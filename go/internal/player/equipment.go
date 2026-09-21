package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

// Equipment is one server-owned equipment instance. The immutable definition
// (name, icon, slot and base stats) remains in the client's local GameData.
type Equipment struct {
	InvenIndex      uint64            `json:"inven_index"`
	ID              uint64            `json:"id"`
	Level           uint64            `json:"level"`
	UseChar         uint64            `json:"use_char,omitempty"`
	KeepFlag        uint64            `json:"keep_flag,omitempty"`
	LockFlag        uint64            `json:"lock_flag,omitempty"`
	SortID          uint64            `json:"sort_id,omitempty"`
	Mark            string            `json:"mark,omitempty"`
	MainOption      []EquipmentOption `json:"main_option,omitempty"`
	SubOption       []EquipmentOption `json:"sub_option,omitempty"`
	PrivateOption   *EquipmentOption  `json:"private_option,omitempty"`
	Rank            []uint64          `json:"rank,omitempty"`
	UpgradeAttempts uint64            `json:"upgrade_attempts"`
}

// EquipmentOption is the exact EquipmentOptionTable composite key used by an
// equipment instance.  The client resolves the displayed stat curve locally.
type EquipmentOption struct {
	GroupID uint64 `json:"group_id"`
	ID      uint64 `json:"id"`
}

type equipmentSnapshot struct {
	Version   string            `json:"version"`
	NextIndex uint64            `json:"next_index"`
	Equipment []Equipment       `json:"equipment"`
	Granted   map[string]uint64 `json:"granted"`
}

// EquipmentInventory persists non-stackable equipment independently from
// ItemDBInfo inventory because the wire protocols are different types.
type EquipmentInventory struct {
	mu         sync.Mutex
	path       string
	owned      equipmentSnapshot
	characters *CharacterStore
	slots      map[uint64]uint64
	upgrade    *gamedata.EquipmentUpgradeDesign
	wallet     *Wallet
	inventory  *Inventory
}

func (s *EquipmentInventory) AttachUpgrade(design *gamedata.EquipmentUpgradeDesign, wallet *Wallet, inventory *Inventory) error {
	if design == nil || wallet == nil || inventory == nil {
		return errors.New("player: incomplete equipment upgrade configuration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upgrade, s.wallet, s.inventory = design, wallet, inventory
	return nil
}

func (s *EquipmentInventory) AttachSlots(slots map[uint64]uint64) error {
	if len(slots) == 0 {
		return errors.New("player: empty equipment slot design")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slots = make(map[uint64]uint64, len(slots))
	for id, slot := range slots {
		if id == 0 || slot > 4 {
			return errors.New("player: invalid equipment slot design")
		}
		s.slots[id] = slot
	}
	return nil
}

func (s *EquipmentInventory) AttachCharacters(characters *CharacterStore) error {
	if characters == nil {
		return errors.New("player: nil character store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.characters = characters
	return nil
}

func OpenEquipmentInventory(path string) (*EquipmentInventory, error) {
	s := &EquipmentInventory{path: filepath.Clean(path), owned: equipmentSnapshot{
		Version: "2.34.13", NextIndex: 910000001, Granted: map[string]uint64{},
	}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var shape struct {
		Equipment []map[string]json.RawMessage `json:"equipment"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		return nil, fmt.Errorf("player: decode equipment shape: %w", err)
	}
	for _, entry := range shape.Equipment {
		if _, present := entry["upgrade_attempts"]; !present {
			return nil, errors.New("player: equipment save requires upgrade_attempts; migrate the development save")
		}
	}
	if err := json.Unmarshal(data, &s.owned); err != nil {
		return nil, fmt.Errorf("player: decode equipment: %w", err)
	}
	if s.owned.Version != "2.34.13" || s.owned.NextIndex < 910000001 || s.owned.Granted == nil {
		return nil, errors.New("player: invalid saved equipment")
	}
	for _, entry := range s.owned.Equipment {
		if len(entry.Rank) != 3 {
			return nil, fmt.Errorf("player: equipment %d requires exactly three rank slots, found %d; repair the development save before starting", entry.InvenIndex, len(entry.Rank))
		}
	}
	return s, nil
}

// GrantOnce returns the same instance on a retry, allowing QuestClear response
// retries without duplicating ownership.
func (s *EquipmentInventory) GrantOnce(identity string, equipmentID uint64) (Equipment, error) {
	if identity == "" || equipmentID == 0 {
		return Equipment{}, errors.New("player: invalid equipment grant")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if index := s.owned.Granted[identity]; index != 0 {
		for _, current := range s.owned.Equipment {
			if current.InvenIndex == index {
				return current, nil
			}
		}
		return Equipment{}, errors.New("player: equipment grant index is missing")
	}
	return s.grantLocked(identity, Equipment{ID: equipmentID})
}

// GrantGeneratedOnce saves an independently generated gacha instance. Retry
// calls return the original rolls rather than creating a second copy.
func (s *EquipmentInventory) GrantGeneratedOnce(identity string, entry Equipment) (Equipment, error) {
	if identity == "" || entry.ID == 0 || len(entry.Rank) != 3 {
		return Equipment{}, errors.New("player: invalid generated equipment")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grantLocked(identity, entry)
}

func (s *EquipmentInventory) grantLocked(identity string, entry Equipment) (Equipment, error) {
	if index := s.owned.Granted[identity]; index != 0 {
		for _, current := range s.owned.Equipment {
			if current.InvenIndex == index {
				return current, nil
			}
		}
		return Equipment{}, errors.New("player: equipment grant index is missing")
	}
	if len(entry.Rank) == 0 {
		// Rank is a fixed three-slot client field (unlock thresholds +3/+6/+9).
		// Even an unenhanced item must have three explicit zero values.
		entry.Rank = []uint64{0, 0, 0}
	}
	if len(entry.Rank) != 3 {
		return Equipment{}, errors.New("player: equipment requires exactly three rank slots")
	}
	entry.InvenIndex = s.owned.NextIndex
	next := equipmentSnapshot{Version: s.owned.Version, NextIndex: s.owned.NextIndex + 1,
		Equipment: append([]Equipment(nil), s.owned.Equipment...), Granted: make(map[string]uint64, len(s.owned.Granted)+1)}
	next.Equipment = append(next.Equipment, entry)
	for k, v := range s.owned.Granted {
		next.Granted[k] = v
	}
	next.Granted[identity] = entry.InvenIndex
	data, err := json.Marshal(next)
	if err != nil {
		return Equipment{}, err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Equipment{}, err
	}
	f, err := os.CreateTemp(dir, ".equipment-*.tmp")
	if err != nil {
		return Equipment{}, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return Equipment{}, fmt.Errorf("player: persist equipment: %w", err)
	}
	s.owned = next
	return entry, nil
}

func EquipmentWire(entry Equipment) []byte {
	base := wire.AppendVarint(nil, 1, entry.ID)
	if entry.Level != 0 {
		base = wire.AppendVarint(base, 2, entry.Level)
	}
	for _, option := range entry.MainOption {
		base = wire.AppendBytes(base, 3, equipmentOptionWire(option))
	}
	for _, option := range entry.SubOption {
		base = wire.AppendBytes(base, 4, equipmentOptionWire(option))
	}
	if entry.PrivateOption != nil {
		base = wire.AppendBytes(base, 5, equipmentOptionWire(*entry.PrivateOption))
	}
	for _, rank := range entry.Rank {
		base = wire.AppendVarint(base, 6, rank)
	}
	var out []byte
	out = wire.AppendVarint(out, 1, entry.InvenIndex)
	if entry.UseChar != 0 {
		out = wire.AppendVarint(out, 2, entry.UseChar)
	}
	if entry.KeepFlag != 0 {
		out = wire.AppendVarint(out, 3, entry.KeepFlag)
	}
	if entry.LockFlag != 0 {
		out = wire.AppendVarint(out, 4, entry.LockFlag)
	}
	out = wire.AppendBytes(out, 5, base)
	if entry.SortID != 0 {
		out = wire.AppendVarint(out, 7, entry.SortID)
	}
	if entry.Mark != "" {
		out = wire.AppendBytes(out, 8, []byte(entry.Mark))
	}
	return out
}

func equipmentOptionWire(option EquipmentOption) []byte {
	out := wire.AppendVarint(nil, 1, option.GroupID)
	return wire.AppendVarint(out, 2, option.ID)
}

func (s *EquipmentInventory) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/EquipInfo" && path != "/EquipUse" && path != "/EquipClear" && path != "/EquipChange" && path != "/EquipUpgrade" && path != "/EquipSequenceUpgrade" && path != "/EquipMarkSet" && path != "/EquipMarkDelete" && path != "/EquipLock" {
		return 0, nil, false, nil
	}
	if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("player: %s invalid sequence", path)
	}
	if path == "/EquipUse" {
		return s.use(request)
	}
	if path == "/EquipClear" {
		return s.clear(request)
	}
	if path == "/EquipUpgrade" {
		return s.upgradeOnce(request)
	}
	if path == "/EquipSequenceUpgrade" {
		return s.upgradeSequence(request)
	}
	if path == "/EquipChange" {
		return s.change(request)
	}
	if path == "/EquipMarkSet" || path == "/EquipMarkDelete" {
		return s.mark(path, request)
	}
	if path == "/EquipLock" {
		return s.lock(request)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var response []byte
	for _, entry := range s.owned.Equipment {
		response = wire.AppendBytes(response, 1, EquipmentWire(entry))
	}
	return 34, response, true, nil
}

const (
	equipUpgradeSuccess = iota
	equipUpgradeFail
	equipUpgradeStopMaxLevel
	equipUpgradeStopSuccess
	equipUpgradeStopNotEnough
	equipUpgradeStopGoldLimit
	equipUpgradeStopTargetLevel
	equipUpgradeStopMaxTryCount
)

func (s *EquipmentInventory) upgradeOnce(request []byte) (int, []byte, bool, error) {
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipUpgrade missing equipment")
	}
	var materials []Item
	err = wire.Walk(request, func(field wire.Field) error {
		if field.Number != 3 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: EquipUpgrade invalid material")
		}
		var item Item
		if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 9: &item.SortID, 10: &item.UseCount}); err != nil {
			return err
		}
		if item.Type == 0 || item.Count == 0 || (item.Type == 4 && (item.ID != 0 || item.InvenIndex != 0)) || (item.Type != 4 && (item.ID == 0 || item.InvenIndex == 0)) {
			return errors.New("player: EquipUpgrade invalid material")
		}
		materials = append(materials, item)
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	if len(materials) == 0 {
		return 0, nil, true, errors.New("player: EquipUpgrade has no material")
	}
	s.mu.Lock()
	entry, success, _, _, err := s.attemptUpgradeLocked(index, materials)
	s.mu.Unlock()
	if err != nil {
		return 0, nil, true, err
	}
	result := uint64(equipUpgradeFail)
	if success {
		result = equipUpgradeSuccess
	}
	response := wire.AppendBytes(nil, 1, EquipmentWire(entry))
	if result != 0 {
		response = wire.AppendVarint(response, 2, result)
	}
	if character, ok := s.equippedCharacter(entry); ok {
		response = wire.AppendBytes(response, 3, CharacterWire(character))
	}
	return 37, response, true, nil
}

func (s *EquipmentInventory) upgradeSequence(request []byte) (int, []byte, bool, error) {
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipSequenceUpgrade missing equipment")
	}
	count, found, err := wire.Varint(request, 3)
	if err != nil || !found || count == 0 || count > 100000 {
		return 0, nil, true, errors.New("player: EquipSequenceUpgrade invalid attempt count")
	}
	goldLimit, _, err := wire.Varint(request, 5)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipSequenceUpgrade invalid gold limit")
	}
	target, _, err := wire.Varint(request, 6)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipSequenceUpgrade invalid target")
	}
	s.mu.Lock()
	if s.upgrade == nil || s.wallet == nil {
		s.mu.Unlock()
		return 0, nil, true, errors.New("player: equipment upgrade unavailable")
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: EquipSequenceUpgrade unknown equipment %d", index)
	}
	maximum := s.upgrade.MaxLevel[s.owned.Equipment[position].ID]
	if target == 0 || target > maximum {
		target = maximum
	}
	var attempts, usedGold uint64
	result := uint64(equipUpgradeStopMaxTryCount)
	var consumed []Item
	var lack []Item
	for attempts < count {
		entry := s.owned.Equipment[position]
		if entry.Level >= maximum {
			result = equipUpgradeStopMaxLevel
			break
		}
		if entry.Level >= target {
			result = equipUpgradeStopTargetLevel
			break
		}
		level, _, designErr := s.upgrade.Level(entry.ID, entry.Level)
		if designErr != nil {
			s.mu.Unlock()
			return 0, nil, true, designErr
		}
		materials, gold, costErr := s.selectUpgradeCosts(level.Costs)
		if costErr != nil {
			result = equipUpgradeStopNotEnough
			lack = upgradeLackItems(level.Costs)
			break
		}
		if goldLimit != 0 && usedGold+gold > goldLimit {
			result = equipUpgradeStopGoldLimit
			break
		}
		if gold != 0 && !s.wallet.CanSpendGold(gold) {
			result = equipUpgradeStopNotEnough
			lack = upgradeLackItems(level.Costs)
			break
		}
		updated, success, spent, actual, attemptErr := s.attemptUpgradeLocked(index, materials)
		if attemptErr != nil {
			s.mu.Unlock()
			return 0, nil, true, attemptErr
		}
		attempts++
		usedGold += spent
		consumed = append(consumed, actual...)
		position = s.equipmentPositionLocked(index)
		if success && updated.Level >= target {
			result = equipUpgradeStopTargetLevel
			if updated.Level >= maximum {
				result = equipUpgradeStopMaxLevel
			}
			break
		}
	}
	entry := s.owned.Equipment[position]
	s.mu.Unlock()
	response := wire.AppendBytes(nil, 1, EquipmentWire(entry))
	if character, ok := s.equippedCharacter(entry); ok {
		response = wire.AppendBytes(response, 2, CharacterWire(character))
	}
	response = wire.AppendVarint(response, 3, result)
	response = wire.AppendVarint(response, 4, attempts)
	for _, item := range consumed {
		response = wire.AppendBytes(response, 5, ItemWire(item))
	}
	for _, item := range lack {
		response = wire.AppendBytes(response, 6, ItemWire(item))
	}
	if usedGold != 0 {
		response = wire.AppendVarint(response, 7, usedGold)
	}
	return 170, response, true, nil
}

func upgradeLackItems(costs []gamedata.PromotionCost) []Item {
	items := make([]Item, 0, len(costs))
	for _, cost := range costs {
		items = append(items, Item{ID: cost.ID, Type: cost.Type, Count: cost.Count})
	}
	return items
}

func (s *EquipmentInventory) equipmentPositionLocked(index uint64) int {
	for i := range s.owned.Equipment {
		if s.owned.Equipment[i].InvenIndex == index {
			return i
		}
	}
	return -1
}

func (s *EquipmentInventory) selectUpgradeCosts(costs []gamedata.PromotionCost) ([]Item, uint64, error) {
	var selected []Item
	var gold uint64
	for _, cost := range costs {
		switch cost.Type {
		case 4:
			if cost.ID != 0 || cost.Count == 0 || gold != 0 {
				return nil, 0, errors.New("player: invalid equipment upgrade gold cost")
			}
			gold = cost.Count
			selected = append(selected, Item{Type: 4, Count: cost.Count})
		case 8:
			items, err := s.inventory.SelectMutable(cost.Type, cost.ID, cost.Count)
			if err != nil {
				return nil, 0, err
			}
			selected = append(selected, items...)
		default:
			return nil, 0, fmt.Errorf("player: unsupported equipment upgrade cost type %d", cost.Type)
		}
	}
	return selected, gold, nil
}

func (s *EquipmentInventory) attemptUpgradeLocked(index uint64, materials []Item) (Equipment, bool, uint64, []Item, error) {
	if s.upgrade == nil || s.wallet == nil {
		return Equipment{}, false, 0, nil, errors.New("player: equipment upgrade unavailable")
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {
		return Equipment{}, false, 0, nil, fmt.Errorf("player: unknown equipment %d", index)
	}
	current := s.owned.Equipment[position]
	level, _, err := s.upgrade.Level(current.ID, current.Level)
	if err != nil {
		return Equipment{}, false, 0, nil, err
	}
	want := make(map[[2]uint64]uint64, len(level.Costs))
	for _, cost := range level.Costs {
		want[[2]uint64{cost.Type, cost.ID}] += cost.Count
	}
	got := make(map[[2]uint64]uint64)
	var gold uint64
	var items []Item
	for _, material := range materials {
		got[[2]uint64{material.Type, material.ID}] += material.Count
		if material.Type == 4 {
			if material.InvenIndex != 0 || material.ID != 0 || gold != 0 {
				return Equipment{}, false, 0, nil, errors.New("player: invalid equipment upgrade currency")
			}
			gold = material.Count
		} else {
			items = append(items, material)
		}
	}
	if len(got) != len(want) {
		return Equipment{}, false, 0, nil, fmt.Errorf("player: equipment upgrade material kinds mismatch")
	}
	for key, count := range want {
		if got[key] != count {
			return Equipment{}, false, 0, nil, fmt.Errorf("player: equipment upgrade material %d/%d=%d want=%d", key[0], key[1], got[key], count)
		}
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return Equipment{}, false, 0, nil, errors.New("player: insufficient gold for equipment upgrade")
	}
	if len(items) != 0 {
		if s.inventory == nil {
			return Equipment{}, false, 0, nil, errors.New("player: equipment upgrade inventory unavailable")
		}
		if err := s.inventory.CanConsume(items); err != nil {
			return Equipment{}, false, 0, nil, err
		}
	}
	// Draw before any cross-store write: RNG failure must never charge the
	// player. Ordinary enhancement also unlocks an official grade at the
	// +3/+6/+9 pivots; smelting can later change those grades, but is separate.
	success, err := s.upgrade.Roll(level.SuccessRatio)
	if err != nil {
		return Equipment{}, false, 0, nil, fmt.Errorf("player: roll equipment upgrade: %w", err)
	}
	var rankSlot, rankValue uint64
	if success {
		nextLevel := current.Level + 1
		switch nextLevel {
		case 3:
			rankSlot = 1
		case 6:
			rankSlot = 2
		case 9:
			rankSlot = 3
		}
		if rankSlot != 0 {
			if len(current.Rank) != 3 || current.Rank[rankSlot-1] != 0 {
				return Equipment{}, false, 0, nil, fmt.Errorf("player: equipment %d invalid rank state at +%d", index, nextLevel)
			}
			rankValue, err = s.upgrade.RollRank(current.ID, rankSlot)
			if err != nil {
				return Equipment{}, false, 0, nil, fmt.Errorf("player: roll equipment rank: %w", err)
			}
		}
	}
	attempt := current.UpgradeAttempts + 1
	identity := "equip-upgrade:" + strconv.FormatUint(index, 10) + ":" + strconv.FormatUint(attempt, 10)
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(identity, gold); err != nil {
			return Equipment{}, false, 0, nil, fmt.Errorf("player: spend equipment upgrade gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(items); err != nil {
			return Equipment{}, false, 0, nil, fmt.Errorf("player: consume equipment upgrade items: %w", err)
		}
	}
	next := cloneEquipmentSnapshot(s.owned)
	next.Equipment[position].UpgradeAttempts = attempt
	if success {
		next.Equipment[position].Level++
		if rankSlot != 0 {
			next.Equipment[position].Rank[rankSlot-1] = rankValue
		}
	}
	if err := s.commitLocked(next, "upgrade"); err != nil {
		return Equipment{}, false, 0, nil, err
	}
	return next.Equipment[position], success, gold, materials, nil
}

func (s *EquipmentInventory) equippedCharacter(entry Equipment) (Character, bool) {
	if entry.UseChar == 0 || s.characters == nil {
		return Character{}, false
	}
	return s.characters.Find(entry.UseChar)
}

func (s *EquipmentInventory) clear(request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipClear missing equipment")
	}
	characterIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipClear missing character")
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipClear character store unavailable")
	}
	if _, found := s.characters.Find(characterIndex); !found {
		return 0, nil, true, fmt.Errorf("player: EquipClear unknown character %d", characterIndex)
	}
	s.mu.Lock()
	next := cloneEquipmentSnapshot(s.owned)
	position := -1
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: EquipClear unknown equipment %d", equipmentIndex)
	}
	if next.Equipment[position].UseChar != characterIndex {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: EquipClear equipment %d is not used by character %d", equipmentIndex, characterIndex)
	}
	next.Equipment[position].UseChar = 0
	if err := s.commitLocked(next, "clear"); err != nil {
		s.mu.Unlock()
		return 0, nil, true, err
	}
	s.mu.Unlock()
	// Re-query after the equipment mutation so the shared stat calculator
	// returns HP with the cleared item excluded.
	character, found := s.characters.Find(characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: EquipClear character %d disappeared", characterIndex)
	}
	return 36, wire.AppendBytes(nil, 1, CharacterWire(character)), true, nil
}

func (s *EquipmentInventory) lock(request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipLock missing equipment")
	}
	// LockFlag is int32, but proto3 omits its zero value. A missing field 3 is
	// therefore the normal unlock request; present values are restricted to 1.
	lockFlag, present, err := wire.Varint(request, 3)
	if err != nil || (present && lockFlag != 1) {
		return 0, nil, true, errors.New("player: EquipLock invalid lock flag")
	}
	if !present {
		lockFlag = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneEquipmentSnapshot(s.owned)
	position := -1
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipLock unknown equipment %d", equipmentIndex)
	}
	next.Equipment[position].LockFlag = lockFlag
	if err := s.commitLocked(next, "lock"); err != nil {
		return 0, nil, true, err
	}
	return 38, nil, true, nil
}

func (s *EquipmentInventory) mark(path string, request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, fmt.Errorf("player: %s missing equipment", strings.TrimPrefix(path, "/"))
	}
	mark := ""
	packetCode := 397
	if path == "/EquipMarkSet" {
		raw, present, fieldErr := wire.Bytes(request, 3)
		if fieldErr != nil || !present || !validEquipmentMark(raw) {
			return 0, nil, true, errors.New("player: EquipMarkSet invalid mark")
		}
		mark = string(raw)
		packetCode = 396
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneEquipmentSnapshot(s.owned)
	position := -1
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: %s unknown equipment %d", strings.TrimPrefix(path, "/"), equipmentIndex)
	}
	next.Equipment[position].Mark = mark
	if err := s.commitLocked(next, strings.TrimPrefix(path, "/")); err != nil {
		return 0, nil, true, err
	}
	return packetCode, nil, true, nil
}

// EquipmentInfo.MakeStringCustomMarkData emits either "~<icon>|<color>"
// (icon 1..15) or a text mark followed by "|<color>". Text input is capped
// by the 2-byte MAXIMUM_CUSTOMMARK_TEXT_SIZE in CustomSettingPopupUI; a
// single character is prefixed with '_' so it cannot be confused with an ID.
func validEquipmentMark(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 8 || !utf8.Valid(raw) {
		return false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 2 {
		return false
	}
	color, err := strconv.Atoi(parts[1])
	if err != nil || color < 0 || color > 5 {
		return false
	}
	left := parts[0]
	if strings.HasPrefix(left, "~") {
		icon, err := strconv.Atoi(strings.TrimPrefix(left, "~"))
		return err == nil && icon >= 1 && icon <= 15
	}
	if strings.HasPrefix(left, "_") {
		text := strings.TrimPrefix(left, "_")
		return utf8.RuneCountInString(text) == 1 && len([]byte(text)) <= 2
	}
	return len(left) >= 1 && len([]byte(left)) <= 2
}

func (s *EquipmentInventory) change(request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipChange missing equipment")
	}
	characterIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipChange missing character")
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipChange character store unavailable")
	}
	if _, found := s.characters.Find(characterIndex); !found {
		return 0, nil, true, fmt.Errorf("player: EquipChange unknown character %d", characterIndex)
	}

	s.mu.Lock()
	if len(s.slots) == 0 {
		s.mu.Unlock()
		return 0, nil, true, errors.New("player: EquipChange slot design unavailable")
	}
	next := cloneEquipmentSnapshot(s.owned)
	position := -1
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: EquipChange unknown equipment %d", equipmentIndex)
	}
	selected := next.Equipment[position]
	slot, exists := s.slots[selected.ID]
	if !exists {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: EquipChange equipment design %d not found", selected.ID)
	}
	if selected.UseChar != 0 && selected.UseChar != characterIndex {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: EquipChange equipment %d belongs to another character", equipmentIndex)
	}
	replaced := false
	for i := range next.Equipment {
		current := &next.Equipment[i]
		if current.InvenIndex == equipmentIndex || current.UseChar != characterIndex {
			continue
		}
		currentSlot, known := s.slots[current.ID]
		if !known {
			s.mu.Unlock()
			return 0, nil, true, fmt.Errorf("player: EquipChange equipped design %d not found", current.ID)
		}
		if currentSlot == slot {
			current.UseChar = 0
			replaced = true
		}
	}
	if !replaced {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: EquipChange character %d has no equipment in slot %d", characterIndex, slot)
	}
	next.Equipment[position].UseChar = characterIndex
	if err := s.commitLocked(next, "change"); err != nil {
		s.mu.Unlock()
		return 0, nil, true, err
	}
	s.mu.Unlock()

	// Max HP depends on the now-current equipment set, so build CharInfo only
	// after the atomic equipment save is visible to the shared stat calculator.
	character, found := s.characters.Find(characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: EquipChange character %d disappeared", characterIndex)
	}
	// PacketCodeTypeProto orders EquipChange at 45 (EquipInfo is 34).
	return 45, wire.AppendBytes(nil, 1, CharacterWire(character)), true, nil
}

func cloneEquipmentSnapshot(current equipmentSnapshot) equipmentSnapshot {
	next := equipmentSnapshot{Version: current.Version, NextIndex: current.NextIndex,
		Equipment: append([]Equipment(nil), current.Equipment...), Granted: make(map[string]uint64, len(current.Granted))}
	for key, value := range current.Granted {
		next.Granted[key] = value
	}
	return next
}

func (s *EquipmentInventory) commitLocked(next equipmentSnapshot, operation string) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".equipment-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return fmt.Errorf("player: persist equipment %s: %w", operation, err)
	}
	s.owned = next
	return nil
}

func (s *EquipmentInventory) All() []Equipment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Equipment(nil), s.owned.Equipment...)
}

// Granted returns a prior idempotent grant without creating an item.
func (s *EquipmentInventory) Granted(identity string) (Equipment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.owned.Granted[identity]
	if index == 0 {
		return Equipment{}, false
	}
	for _, entry := range s.owned.Equipment {
		if entry.InvenIndex == index {
			return entry, true
		}
	}
	return Equipment{}, false
}

func (s *EquipmentInventory) use(request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipUse missing equipment")
	}
	characterIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipUse missing character")
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipUse character store unavailable")
	}
	// Find computes account buffs and may query EquipmentInventory.All(). Do
	// not hold the equipment mutex while resolving the character's stats.
	character, found := s.characters.Find(characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: EquipUse unknown character %d", characterIndex)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := equipmentSnapshot{Version: s.owned.Version, NextIndex: s.owned.NextIndex,
		Equipment: append([]Equipment(nil), s.owned.Equipment...), Granted: make(map[string]uint64, len(s.owned.Granted))}
	for k, v := range s.owned.Granted {
		next.Granted[k] = v
	}
	position := -1
	for i, current := range next.Equipment {
		if current.InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipUse unknown equipment %d", equipmentIndex)
	}
	next.Equipment[position].UseChar = characterIndex
	data, err := json.Marshal(next)
	if err != nil {
		return 0, nil, true, err
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".equipment-*.tmp")
	if err != nil {
		return 0, nil, true, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: persist equipment use: %w", err)
	}
	s.owned = next
	return 35, wire.AppendBytes(nil, 1, CharacterWire(character)), true, nil
}
