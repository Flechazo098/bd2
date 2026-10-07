package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/ownership"
	"bd2server/internal/server/protocol/wire"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

func CharacterWire(v Character) []byte {
	out := ownership.Character{InvenIndex: v.InvenIndex, ID: v.ID, HP: v.HP, Level: v.Level, CostumeID: v.CostumeID, Exp: v.Exp, UseCostume: v.UseCostume, TalentLevel: v.TalentLevel, TalentExp: v.TalentExp, SolidarityReward: v.SolidarityReward, ExpiryTime: v.ExpiryTime, ConnectPotentialCostume: v.ConnectPotentialCostume}
	for _, p := range v.Pictorialbook {
		out.Pictorialbook = append(out.Pictorialbook, ownership.Pictorial{ID: p.ID, GroupID: p.GroupID})
	}
	return ownership.EncodeCharacter(out)
}

func (s *CharacterStore) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path == "/CharImmortal" {
		return s.charImmortal(ctx, request)
	}
	if path == "/TalentSkillUpgrade" {
		return s.talentSkillUpgrade(ctx, request)
	}
	if path != "/CharGrowth" {
		return 0, nil, false, nil
	}
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: CharGrowth missing character")
	}
	var materials []assets.Item
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 3 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: invalid growth material")
		}
		var item assets.Item
		if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 9: &item.SortID, 10: &item.UseCount}); err != nil {
			return err
		}
		if item.Type == 0 || item.Count == 0 || (item.Type != 4 && (item.InvenIndex == 0 || item.ID == 0)) {
			return errors.New("player: incomplete growth material")
		}
		materials = append(materials, item)
		return nil
	}); err != nil {
		return 0, nil, true, err
	}
	if len(materials) == 0 {
		return 0, nil, true, errors.New("player: CharGrowth has no materials")
	}

	position := -1
	for i, current := range s.characters {
		if current.InvenIndex == index {
			position = i
			break
		}
	}
	var current Character
	fromCollection := false
	if position >= 0 {
		current = s.characters[position]
	} else if s.collection != nil {
		current, fromCollection = s.collection.FindCharacter(index)
	}
	if position < 0 && !fromCollection {
		return 0, nil, true, fmt.Errorf("player: unknown character inventory index %d", index)
	}
	isPromotion := false
	for _, material := range materials {
		if material.Type == 4 {
			isPromotion = true
			break
		}
	}
	if isPromotion {
		return s.promoteCharacter(ctx, current, position, fromCollection, materials)
	}
	growthMaterials := make([]gamedata.GrowthMaterial, len(materials))
	for i, material := range materials {
		if material.Type != 8 {
			return 0, nil, true, fmt.Errorf("player: unsupported growth material type %d", material.Type)
		}
		growthMaterials[i] = gamedata.GrowthMaterial{ID: material.ID, Count: material.Count}
	}
	newLevel, newExp, refunds, err := s.grow(current, growthMaterials)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: calculate character growth: %w", err)
	}
	current.Level = newLevel
	current.Exp = newExp
	if s.maxHealth != nil {
		maxHealth := s.maxHealth

		hp, healthErr := maxHealth(ctx, current)

		current.HP, err = hp, healthErr
		if err != nil {
			return 0, nil, true, fmt.Errorf("player: calculate grown character maximum health: %w", err)
		}
	}
	returned, err := s.inventory.ConsumeAndRefund(ctx, materials, refunds)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: consume growth material: %w", err)
	}
	if fromCollection {
		if err := s.collection.UpdateCharacter(ctx, current.ID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist collection character growth: %w", err)
		}
	} else {
		next := append([]Character(nil), s.characters...)
		next[position] = current
		if err := s.persist(ctx, next); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist character growth: %w", err)
		}
		s.characters = next
	}
	if err := s.resetCurrentHealth(ctx, current.InvenIndex); err != nil {
		return 0, nil, true, err
	}
	response := wire.AppendBytes(nil, 1, CharacterWire(current))
	var bundle []byte
	for _, item := range returned {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
	}
	response = wire.AppendBytes(response, 2, bundle)
	return 433, response, true, nil
}

func (s *CharAwakeService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	switch path {
	case "/CharAwakeInfo":
		return s.info(request)
	case "/CharImprintLevelUp":
		return s.imprintLevelUp(ctx, request)
	case "/CharAwakeActive":
		return s.awakeActive(ctx, request)
	default:
		return 0, nil, false, nil
	}
}

func (s *CharacterStore) promoteCharacter(ctx command.Context, current Character, position int, fromCollection bool, materials []assets.Item) (int, []byte, bool, error) {
	var items []assets.Item
	requested := make(map[[2]uint64]uint64)
	var gold uint64
	for _, material := range materials {
		if material.Type == 4 { //nolint:staticcheck // QF1003
			if material.InvenIndex != 0 || material.ID != 0 || gold != 0 {
				return 0, nil, true, errors.New("player: invalid promotion currency")
			}
			gold = material.Count
		} else if material.Type == 8 {
			items = append(items, material)
			key := [2]uint64{8, material.ID}
			if material.Count > ^uint64(0)-requested[key] {
				return 0, nil, true, errors.New("player: promotion material overflow")
			}
			requested[key] += material.Count
		} else {
			return 0, nil, true, fmt.Errorf("player: unsupported promotion item type %d", material.Type)
		}
	}
	if gold == 0 {
		return 0, nil, true, errors.New("player: promotion has no gold cost")
	}
	submitted := make([]gamedata.PromotionCost, 0, len(requested)+1)
	for key, count := range requested {
		submitted = append(submitted, gamedata.PromotionCost{Type: key[0], ID: key[1], Count: count})
	}
	submitted = append(submitted, gamedata.PromotionCost{Type: 4, Count: gold})
	result, err := s.promoteGrowth(current, submitted)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: calculate combined character promotion: %w; request=%+v", err, materials)
	}
	if gold != 0 && (s.wallet == nil || !s.wallet.CanSpendGold(gold)) {
		return 0, nil, true, errors.New("player: insufficient gold for promotion")
	}
	if len(items) == 0 {
		return 0, nil, true, errors.New("player: promotion has no item material")
	}
	previousID := current.ID
	current.ID = result.CharacterID
	current.Level = result.Level
	current.Exp = result.Exp
	if fromCollection {
		if err := s.collection.CanUpdateCharacter(ctx, previousID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate promoted collection character: %w", err)
		}
	}
	if err := s.inventory.CanConsume(ctx, items); err != nil {
		return 0, nil, true, fmt.Errorf("player: validate promotion items: %w", err)
	}
	if s.maxHealth != nil {
		maxHealth := s.maxHealth

		hp, healthErr := maxHealth(ctx, current)

		if healthErr != nil {
			return 0, nil, true, fmt.Errorf("player: calculate promoted character health: %w", healthErr)
		}
		current.HP = hp
	}
	if gold != 0 {
		identity := "char-promote:" + strconv.FormatUint(current.InvenIndex, 10) + ":" + strconv.FormatUint(current.ID, 10)
		if _, err := s.wallet.SpendGoldOnce(ctx, identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume promotion gold: %w", err)
		}
	}
	returned, err := s.inventory.ConsumeAndRefund(ctx, items, result.Refunds)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: consume promotion items: %w", err)
	}
	if fromCollection {
		if err := s.collection.UpdateCharacter(ctx, previousID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist promoted collection character: %w", err)
		}
	} else {
		next := append([]Character(nil), s.characters...)
		next[position] = current
		if err := s.persist(ctx, next); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist promoted character: %w", err)
		}
		s.characters = next
	}
	if err := s.resetCurrentHealth(ctx, current.InvenIndex); err != nil {
		return 0, nil, true, err
	}
	response := wire.AppendBytes(nil, 1, CharacterWire(current))
	var bundle []byte
	for _, item := range returned {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
	}
	if len(bundle) != 0 {
		response = wire.AppendBytes(response, 2, bundle)
	}
	return 433, response, true, nil
}

// charImmortal completes the automatic post-battle revival for characters
// whose TalentSkillTable.ClassType is 14. The story character 6010 has
// ValueList[0]=10000 at every talent level (100%). The authoritative maximum
// Maximum HP is calculated separately from the persisted current HP and the
// restored value is saved explicitly for subsequent character snapshots.
func (s *CharacterStore) charImmortal(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, errors.New("player: CharImmortal missing sequence")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(request))

	key := fmt.Sprintf("%s:%d", ctx.SessionID, seq)
	prior, already := s.immortalReplies[key]

	if already {
		if prior.Digest != digest {
			return 0, nil, true, errors.New("player: immortal replay payload changed")
		}
		return prior.Code, append([]byte(nil), prior.Body...), true, nil
	}
	var indices []uint64
	err = wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 0 && field.Type != 2 {
			return errors.New("player: CharImmortal invalid inventory index field")
		}
		for data := field.Value; len(data) != 0; {
			index, count := binary.Uvarint(data)
			if count <= 0 || index == 0 {
				return errors.New("player: CharImmortal invalid inventory index")
			}
			indices = append(indices, index)
			data = data[count:]
		}
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	if len(indices) == 0 || len(indices) > 5 {
		return 0, nil, true, fmt.Errorf("player: CharImmortal invalid character count %d", len(indices))
	}
	seen := make(map[uint64]bool, len(indices))
	var response []byte
	for _, index := range indices {
		if seen[index] {
			return 0, nil, true, fmt.Errorf("player: CharImmortal duplicate character %d", index)
		}
		seen[index] = true
		character, found := s.Find(ctx, index)
		if !found {
			return 0, nil, true, fmt.Errorf("player: CharImmortal unknown character %d", index)
		}
		if character.HP != 0 {
			return 0, nil, true, errors.New("player: immortal requires a defeated character")
		}
		if !s.immortal.CanRestore(character.ID, character.TalentLevel) {
			return 0, nil, true, errors.New("player: character has no supported immortal talent at current level")
		}
		maximum, err := s.MaxHealth(ctx, index)
		if err != nil {
			return 0, nil, true, err
		}
		character.HP = maximum
		response = wire.AppendBytes(response, 1, CharacterWire(character))
	}
	for _, index := range indices {
		maximum, err := s.MaxHealth(ctx, index)
		if err != nil {
			return 0, nil, true, err
		}
		if err := s.SetCurrentHealth(ctx, index, maximum); err != nil {
			return 0, nil, true, err
		}
	}

	if s.immortalReplies == nil {
		s.immortalReplies = map[string]talentUpgradeReply{}
	}
	if len(s.immortalReplies) >= 1024 {
		s.immortalReplies = map[string]talentUpgradeReply{}
	}
	s.immortalReplies[key] = talentUpgradeReply{Digest: digest, Code: 96, Body: append([]byte(nil), response...)}

	return 96, response, true, nil
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
	slices.Sort(ids)
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

func decodeAwakeMaterials(request []byte, fieldNumber int) ([]assets.Item, error) {
	var materials []assets.Item
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != fieldNumber {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: invalid character awakening material field")
		}
		var item assets.Item
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

func (s *CharAwakeService) imprintLevelUp(ctx command.Context, request []byte) (int, []byte, bool, error) {
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
	character, found := s.characters.Find(ctx, characterIndex)
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
		if err := s.inventory.CanConsume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate imprint inventory: %w", err)
		}
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for character imprint")
	}
	identity := "char-imprint:" + strconv.FormatUint(uniqueID, 10)
	for _, level := range levels {
		identity += ":" + strconv.FormatUint(level, 10) //nolint:modernize // stringsbuilder
	}
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(ctx, identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend imprint gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume imprint materials: %w", err)
		}
	}
	next := current
	next.ImprintLevels = levels
	if err := s.collection.UpdateCharAwake(ctx, uniqueID, current, next); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist character imprint: %w", err)
	}
	return 327, nil, true, nil
}

func (s *CharAwakeService) awakeActive(ctx command.Context, request []byte) (int, []byte, bool, error) {
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
	character, found := s.characters.Find(ctx, characterIndex)
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
		if err := s.inventory.CanConsume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate awakening inventory: %w", err)
		}
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for character awakening")
	}
	identity := "char-awake:" + strconv.FormatUint(uniqueID, 10)
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(ctx, identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend awakening gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume awakening materials: %w", err)
		}
	}
	next := current
	next.IsAwake = true
	if err := s.collection.UpdateCharAwake(ctx, uniqueID, current, next); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist character awakening: %w", err)
	}
	return 328, nil, true, nil
}
