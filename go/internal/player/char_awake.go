package player

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

type CharAwakeService struct {
	design     *gamedata.CharAwakeDesign
	collection *CollectionStore
	characters *CharacterStore
	inventory  *Inventory
	wallet     *Wallet
}

func NewCharAwakeService(design *gamedata.CharAwakeDesign, collection *CollectionStore, characters *CharacterStore, inventory *Inventory, wallet *Wallet) (*CharAwakeService, error) {
	if design == nil || collection == nil || characters == nil || inventory == nil || wallet == nil {
		return nil, errors.New("player: incomplete character awakening service")
	}
	for uniqueID, progress := range collection.CharAwakeStates() {
		if err := design.ValidateProgress(uniqueID, progress.ImprintLevels, progress.IsAwake); err != nil {
			return nil, fmt.Errorf("player: invalid saved character awakening progress: %w", err)
		}
	}
	return &CharAwakeService{design: design, collection: collection, characters: characters, inventory: inventory, wallet: wallet}, nil
}

func (s *CharAwakeService) Handle(path string, request []byte) (int, []byte, bool, error) {
	switch path {
	case "/CharAwakeInfo":
		return s.info(request)
	case "/CharImprintLevelUp":
		return s.imprintLevelUp(request)
	case "/CharAwakeActive":
		return s.awakeActive(request)
	default:
		return 0, nil, false, nil
	}
}

func validAwakeSequence(request []byte, path string) error {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return fmt.Errorf("player: %s missing sequence", path)
	}
	return nil
}

func (s *CharAwakeService) info(request []byte) (int, []byte, bool, error) {
	if err := validAwakeSequence(request, "CharAwakeInfo"); err != nil {
		return 0, nil, true, err
	}
	states := s.collection.CharAwakeStates()
	ids := make([]uint64, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var response []byte
	for _, id := range ids {
		progress := states[id]
		var entry []byte
		entry = wire.AppendVarint(entry, 1, id)
		for i, level := range progress.ImprintLevels {
			if level != 0 {
				entry = wire.AppendVarint(entry, 2+i, level)
			}
		}
		if progress.IsAwake {
			entry = wire.AppendVarint(entry, 5, 1)
		}
		response = wire.AppendBytes(response, 1, entry)
	}
	return 326, response, true, nil
}

func decodeAwakeMaterials(request []byte, fieldNumber int) ([]Item, error) {
	var materials []Item
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != fieldNumber {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: invalid character awakening material field")
		}
		var item Item
		if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 9: &item.SortID, 10: &item.UseCount}); err != nil {
			return err
		}
		if item.Type == 0 || item.Count == 0 || (item.Type != 4 && (item.InvenIndex == 0 || item.ID == 0)) {
			return errors.New("player: incomplete character awakening material")
		}
		materials = append(materials, item)
		return nil
	})
	return materials, err
}

func (s *CharAwakeService) imprintLevelUp(request []byte) (int, []byte, bool, error) {
	if err := validAwakeSequence(request, "CharImprintLevelUp"); err != nil {
		return 0, nil, true, err
	}
	characterIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: CharImprintLevelUp missing character")
	}
	var targets []gamedata.CharImprintTarget
	err = wire.Walk(request, func(field wire.Field) error {
		if field.Number != 3 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: invalid imprint target field")
		}
		var target gamedata.CharImprintTarget
		if err := decodeVarints(field.Value, map[int]*uint64{1: &target.Slot, 2: &target.TargetLevel}); err != nil {
			return err
		}
		if target.Slot == 0 || target.TargetLevel == 0 {
			return errors.New("player: incomplete imprint target")
		}
		targets = append(targets, target)
		return nil
	})
	if err != nil || len(targets) == 0 {
		if err == nil {
			err = errors.New("player: CharImprintLevelUp has no targets")
		}
		return 0, nil, true, err
	}
	materials, err := decodeAwakeMaterials(request, 4)
	if err != nil || len(materials) == 0 {
		if err == nil {
			err = errors.New("player: CharImprintLevelUp has no materials")
		}
		return 0, nil, true, err
	}
	character, found := s.characters.Find(characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: unknown imprint character %d", characterIndex)
	}
	uniqueID, err := s.design.ValidateGrowthCompleted(character.ID, character.Level)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: validate imprint character growth: %w", err)
	}
	current, _ := s.collection.CharAwakeState(uniqueID)
	costs, levels, err := s.design.ImprintCosts(uniqueID, current.ImprintLevels, targets)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: validate imprint GameData: %w", err)
	}
	items, gold, err := s.validateCosts(costs, materials)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: validate imprint materials: %w", err)
	}
	if len(items) != 0 {
		if err := s.inventory.CanConsume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate imprint inventory: %w", err)
		}
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for character imprint")
	}
	identity := "char-imprint:" + strconv.FormatUint(uniqueID, 10)
	for _, level := range levels {
		identity += ":" + strconv.FormatUint(level, 10)
	}
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend imprint gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume imprint materials: %w", err)
		}
	}
	next := current
	next.ImprintLevels = levels
	if err := s.collection.UpdateCharAwake(uniqueID, current, next); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist character imprint: %w", err)
	}
	return 327, nil, true, nil
}

func (s *CharAwakeService) awakeActive(request []byte) (int, []byte, bool, error) {
	if err := validAwakeSequence(request, "CharAwakeActive"); err != nil {
		return 0, nil, true, err
	}
	characterIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: CharAwakeActive missing character")
	}
	materials, err := decodeAwakeMaterials(request, 3)
	if err != nil || len(materials) == 0 {
		if err == nil {
			err = errors.New("player: CharAwakeActive has no materials")
		}
		return 0, nil, true, err
	}
	character, found := s.characters.Find(characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: unknown awakening character %d", characterIndex)
	}
	uniqueID, err := s.design.ValidateGrowthCompleted(character.ID, character.Level)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: validate awakening character growth: %w", err)
	}
	current, _ := s.collection.CharAwakeState(uniqueID)
	costs, err := s.design.AwakeCosts(uniqueID, current.ImprintLevels, current.IsAwake)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: validate awakening GameData: %w", err)
	}
	items, gold, err := s.validateCosts(costs, materials)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: validate awakening materials: %w", err)
	}
	if len(items) != 0 {
		if err := s.inventory.CanConsume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate awakening inventory: %w", err)
		}
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for character awakening")
	}
	identity := "char-awake:" + strconv.FormatUint(uniqueID, 10)
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend awakening gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume awakening materials: %w", err)
		}
	}
	next := current
	next.IsAwake = true
	if err := s.collection.UpdateCharAwake(uniqueID, current, next); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist character awakening: %w", err)
	}
	return 328, nil, true, nil
}

func (s *CharAwakeService) validateCosts(costs []gamedata.CharAwakeCost, materials []Item) ([]Item, uint64, error) {
	want := make(map[[2]uint64]uint64, len(costs))
	for _, cost := range costs {
		key := [2]uint64{cost.Type, cost.ID}
		if cost.Count > ^uint64(0)-want[key] {
			return nil, 0, errors.New("character awakening cost overflow")
		}
		want[key] += cost.Count
	}
	got := make(map[[2]uint64]uint64, len(materials))
	var items []Item
	var gold uint64
	for _, material := range materials {
		key := [2]uint64{material.Type, material.ID}
		switch material.Type {
		case 4:
			if material.InvenIndex != 0 || material.ID != 0 || gold != 0 {
				return nil, 0, errors.New("invalid character awakening currency")
			}
			gold = material.Count
		case 8:
			items = append(items, material)
		default:
			return nil, 0, fmt.Errorf("unsupported character awakening material type %d", material.Type)
		}
		if material.Count > ^uint64(0)-got[key] {
			return nil, 0, errors.New("submitted character awakening material overflow")
		}
		got[key] += material.Count
	}
	if len(got) != len(want) {
		return nil, 0, fmt.Errorf("material kinds mismatch: request=%v GameData=%v", got, want)
	}
	for key, count := range want {
		if got[key] != count {
			return nil, 0, fmt.Errorf("material %d/%d=%d want %d", key[0], key[1], got[key], count)
		}
	}
	return items, gold, nil
}

// Contributions exposes server-side derived stats for CharInfo, revival and
// battle calculators. The client independently computes the same values from
// CharAwakeInfo, so neither side relies on a persisted derived number.
func (s *CharAwakeService) Contributions(character Character) ([]gamedata.StatContribution, error) {
	uniqueID, ok := s.design.CharacterUniqueID(character.ID)
	if !ok {
		return nil, nil
	}
	progress, _ := s.collection.CharAwakeState(uniqueID)
	return s.design.CharAwakeContributions(uniqueID, progress.ImprintLevels, progress.IsAwake)
}
