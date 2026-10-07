package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"unicode/utf8"
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
	Equipment []Equipment       `json:"-"`
	Granted   map[string]uint64 `json:"-"`
}

// EquipmentInventory persists non-stackable equipment independently from
// ItemDBInfo inventory because the wire protocols are different types.
type EquipmentInventory struct {
	store        stateio.ScopedEntryStore
	owned        equipmentSnapshot
	persisted    equipmentSnapshot
	corePresent  bool
	characters   EquipmentCharacterSource
	slots        map[uint64]uint64
	upgrade      *gamedata.EquipmentUpgradeDesign
	craft        *gamedata.EquipmentCraftDesign
	smelting     *gamedata.EquipmentSmeltingDesign
	optionReroll *gamedata.EquipmentOptionRerollDesign
	statDesign   *gamedata.EquipmentStatDesign
	wallet       *Wallet
	inventory    *Inventory

	smeltCache    map[string]smeltingReply
	pendingReroll *equipmentOptionRerollPending
	presets       map[equipmentPresetKey]equipmentPreset
}

type smeltingReply struct {
	code int
	body []byte
}

type equipmentOptionRerollPending struct {
	Equipment Equipment `json:"equipment"`
}

func (s *EquipmentInventory) AttachUpgrade(ctx command.Context, design *gamedata.EquipmentUpgradeDesign, wallet *Wallet, inventory *Inventory) error {
	if design == nil || wallet == nil || inventory == nil {
		return errors.New("player: incomplete equipment upgrade configuration")
	}

	s.upgrade, s.wallet, s.inventory = design, wallet, inventory
	return nil
}

func (s *EquipmentInventory) AttachCraft(ctx command.Context, design *gamedata.EquipmentCraftDesign) error {
	if design == nil {
		return errors.New("player: incomplete equipment crafting configuration")
	}

	s.craft = design
	return nil
}

func (s *EquipmentInventory) AttachSmelting(ctx command.Context, design *gamedata.EquipmentSmeltingDesign, wallet *Wallet, inventory *Inventory) error {
	if design == nil || wallet == nil || inventory == nil {
		return errors.New("player: incomplete equipment smelting configuration")
	}

	s.smelting, s.wallet, s.inventory = design, wallet, inventory
	return nil
}

func (s *EquipmentInventory) AttachOptionReroll(ctx command.Context, design *gamedata.EquipmentOptionRerollDesign, wallet *Wallet, inventory *Inventory) error {
	if design == nil || wallet == nil || inventory == nil {
		return errors.New("player: incomplete equipment option reroll configuration")
	}

	s.optionReroll, s.wallet, s.inventory = design, wallet, inventory
	if s.pendingReroll != nil {
		if err := s.validatePendingRerollLocked(s.pendingReroll); err != nil {
			return err
		}
	}
	return nil
}

// BeginLogin resets the in-memory request replay cache. A repeated protobuf
// sequence in one login must return the first refinement result without a
// second roll or charge.
func (s *EquipmentInventory) BeginLogin(ctx command.Context) {
	id := ctx.SessionID

	if id == "" {
		return
	}
	s.smeltCache = make(map[string]smeltingReply)
}

func (s *EquipmentInventory) AttachSlots(ctx command.Context, slots map[uint64]uint64) error {
	if len(slots) == 0 {
		return errors.New("player: empty equipment slot design")
	}

	s.slots = make(map[uint64]uint64, len(slots))
	for id, slot := range slots {
		if id == 0 || slot > 4 {
			return errors.New("player: invalid equipment slot design")
		}
		s.slots[id] = slot
	}
	return nil
}

func (s *EquipmentInventory) AttachCharacters(ctx command.Context, characters EquipmentCharacterSource) error {
	if characters == nil {
		return errors.New("player: nil character store")
	}
	known := make(map[uint64]bool)
	for _, character := range characters.EquipmentCharacters() {
		known[character.InvenIndex] = true
	}

	for key := range s.presets {
		if !known[key.CharacterIndex] {
			return fmt.Errorf("player: equipment preset references unknown character %d", key.CharacterIndex)
		}
	}
	s.characters = characters
	return nil
}

func OpenEquipmentInventory(ctx command.Context, store stateio.Store) (*EquipmentInventory, error) {
	entries, ok := store.(stateio.ScopedEntryStore)
	if !ok {
		return nil, errors.New("player: nil equipment store")
	}
	s := &EquipmentInventory{store: entries, smeltCache: make(map[string]smeltingReply), presets: make(map[equipmentPresetKey]equipmentPreset), owned: equipmentSnapshot{
		Version: versionconfig.State(), NextIndex: 910000001, Granted: map[string]uint64{},
	}}
	data, err := store.Load(ctx.State, "equipment")
	if err != nil {
		return nil, err
	}
	if data != nil {
		s.corePresent = true
		if err := stateio.RequireExactJSONObject(data, "version", "next_index"); err != nil {
			return nil, fmt.Errorf("player: incompatible equipment layout: %w", err)
		}
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(data, &shape); err != nil {
			return nil, fmt.Errorf("player: decode equipment shape: %w", err)
		}
		for _, name := range []string{"equipment", "granted"} {
			if _, exists := shape[name]; exists {
				return nil, fmt.Errorf("player: equipment %s must use entries", name)
			}
		}
		if err := json.Unmarshal(data, &s.owned); err != nil {
			return nil, fmt.Errorf("player: decode equipment: %w", err)
		}
	} else if err := stateio.RequireNoEntries(entries, ctx.State, "equipment", "equipment", "granted", "reroll_pending", "presets"); err != nil {
		return nil, fmt.Errorf("player: invalid equipment storage: %w", err)
	}
	if s.owned.Version != versionconfig.State() || s.owned.NextIndex < 910000001 {
		return nil, errors.New("player: invalid saved equipment")
	}
	s.owned.Granted, err = loadUintEntries(ctx, entries, "equipment", "granted")
	if err != nil {
		return nil, err
	}
	rawEquipment, err := entries.ListEntries(ctx.State, "equipment", "equipment")
	if err != nil {
		return nil, err
	}
	for key, value := range rawEquipment {
		var entry Equipment
		index, parseErr := strconv.ParseUint(key, 10, 64)
		var shape map[string]json.RawMessage
		if parseErr != nil || json.Unmarshal(value, &shape) != nil || json.Unmarshal(value, &entry) != nil || entry.InvenIndex != index {
			return nil, fmt.Errorf("player: invalid equipment entry %q", key)
		}
		if _, present := shape["upgrade_attempts"]; !present {
			return nil, errors.New("player: equipment save requires upgrade_attempts; migrate the development save")
		}
		s.owned.Equipment = append(s.owned.Equipment, entry)
	}
	sort.Slice(s.owned.Equipment, func(i, j int) bool { return s.owned.Equipment[i].InvenIndex < s.owned.Equipment[j].InvenIndex })
	for _, entry := range s.owned.Equipment {
		if len(entry.Rank) != 3 {
			return nil, fmt.Errorf("player: equipment %d requires exactly three rank slots, found %d; repair the development save before starting", entry.InvenIndex, len(entry.Rank))
		}
	}
	pendingEntries, err := entries.ListEntries(ctx.State, "equipment", "reroll_pending")
	if err != nil {
		return nil, err
	}
	if len(pendingEntries) > 1 {
		return nil, errors.New("player: multiple equipment option reroll candidates")
	}
	if raw, ok := pendingEntries["current"]; ok {
		if err := stateio.RequireExactJSONObject(raw, "equipment"); err != nil {
			return nil, fmt.Errorf("player: incompatible equipment option reroll candidate: %w", err)
		}
		var pending equipmentOptionRerollPending
		if err := json.Unmarshal(raw, &pending); err != nil {
			return nil, fmt.Errorf("player: decode equipment option reroll candidate: %w", err)
		}
		s.pendingReroll = &pending
		if err := s.validatePendingRerollLocked(s.pendingReroll); err != nil {
			return nil, err
		}
	} else if len(pendingEntries) != 0 {
		return nil, errors.New("player: invalid equipment option reroll candidate key")
	}
	if err := s.loadEquipmentPresets(ctx, entries); err != nil {
		return nil, err
	}
	s.persisted = cloneEquipmentSnapshot(s.owned)
	return s, nil
}

func (s *EquipmentInventory) EnsurePersisted(ctx command.Context) error {

	data, err := s.store.Load(ctx.State, "equipment")
	if err != nil {
		return err
	}
	if data != nil {
		return nil
	}
	return s.commitLocked(ctx, cloneEquipmentSnapshot(s.owned), "initial account generation")
}

// GrantOnce returns the same instance on a retry, allowing QuestClear response
// retries without duplicating ownership.
func (s *EquipmentInventory) GrantOnce(ctx command.Context, identity string, equipmentID uint64) (Equipment, error) {
	if identity == "" || equipmentID == 0 {
		return Equipment{}, errors.New("player: invalid equipment grant")
	}

	if index := s.owned.Granted[identity]; index != 0 {
		for _, current := range s.owned.Equipment {
			if current.InvenIndex == index {
				return current, nil
			}
		}
		return Equipment{}, errors.New("player: equipment grant index is missing")
	}
	return s.grantLocked(ctx, identity, Equipment{ID: equipmentID})
}

// GrantGeneratedOnce saves an independently generated gacha instance. Retry
// calls return the original rolls rather than creating a second copy.
func (s *EquipmentInventory) GrantGeneratedOnce(ctx command.Context, identity string, entry Equipment) (Equipment, error) {
	if identity == "" || entry.ID == 0 || len(entry.Rank) != 3 {
		return Equipment{}, errors.New("player: invalid generated equipment")
	}

	return s.grantLocked(ctx, identity, entry)
}

func (s *EquipmentInventory) grantLocked(ctx command.Context, identity string, entry Equipment) (Equipment, error) {
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
	maps.Copy(next.Granted, s.owned.Granted)
	next.Granted[identity] = entry.InvenIndex
	if err := s.commitLocked(ctx, next, "grant"); err != nil {
		return Equipment{}, err
	}
	return entry, nil
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

func (s *EquipmentInventory) smeltingCacheKey(ctx command.Context, kind string, seq uint64) string {
	return kind + ":" + ctx.SessionID + ":seq:" + strconv.FormatUint(seq, 10)
}

func (s *EquipmentInventory) smeltingEquipmentLocked(index uint64) (int, Equipment, error) {
	if s.smelting == nil || s.wallet == nil || s.inventory == nil {
		return -1, Equipment{}, errors.New("player: equipment smelting unavailable")
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {
		return -1, Equipment{}, fmt.Errorf("player: unknown equipment %d", index)
	}
	entry := s.owned.Equipment[position]
	design, ok := s.smelting.Equipment[entry.ID]
	if !ok || entry.Level != design.MaxLevel || len(entry.Rank) != 3 {
		return -1, Equipment{}, fmt.Errorf("player: equipment %d is not ready for smelting", index)
	}
	if _, err := s.smelting.Score(entry.ID, entry.Rank); err != nil {
		return -1, Equipment{}, fmt.Errorf("player: equipment %d has invalid smelting rank: %w", index, err)
	}
	return position, entry, nil
}

func validateSmeltingMaterials(costs []gamedata.PromotionCost, materials []Item) (gold, mileageMaterial uint64, err error) {
	want := make(map[[2]uint64]uint64, len(costs))
	for _, cost := range costs {
		want[[2]uint64{cost.Type, cost.ID}] += cost.Count
	}
	got := make(map[[2]uint64]uint64, len(materials))
	for _, item := range materials {
		got[[2]uint64{item.Type, item.ID}] += item.Count
		if item.Type == 4 {
			gold += item.Count
		} else {
			mileageMaterial += item.Count
		}
	}
	if len(got) != len(want) {
		return 0, 0, errors.New("player: equipment smelting material kinds mismatch")
	}
	for key, count := range want {
		if got[key] != count {
			return 0, 0, fmt.Errorf("player: equipment smelting material %d/%d=%d want=%d", key[0], key[1], got[key], count)
		}
	}
	return gold, mileageMaterial, nil
}

func (s *EquipmentInventory) selectSmeltingCosts(ctx command.Context, costs []gamedata.PromotionCost, attempts uint64) ([]Item, uint64, error) {
	var result []Item
	var gold uint64
	for _, cost := range costs {
		if attempts != 0 && cost.Count > ^uint64(0)/attempts {
			return nil, 0, errors.New("player: equipment smelting cost overflow")
		}
		count := cost.Count * attempts
		switch cost.Type {
		case 4:
			if cost.ID != 0 || gold != 0 || !s.wallet.CanSpendGold(count) {
				return nil, 0, errors.New("player: insufficient equipment smelting gold")
			}
			gold = count
			result = append(result, Item{Type: 4, Count: count})
		case 8:
			items, err := s.inventory.SelectMutable(ctx, cost.Type, cost.ID, count)
			if err != nil {
				return nil, 0, err
			}
			result = append(result, items...)
		default:
			return nil, 0, fmt.Errorf("player: unsupported equipment smelting cost type %d", cost.Type)
		}
	}
	return result, gold, nil
}

// commitSmeltingLocked keeps refinement's three typed snapshots synchronized
// in memory. Caller holds equipment.mu; this method takes the remaining locks
// in wallet -> inventory order, calculates every candidate before writing,
// then publishes all three only after their saves succeed. The surrounding
// request transaction supplies durable all-or-none recovery across the writes.
func (s *EquipmentInventory) commitSmeltingLocked(ctx command.Context,
	nextEquipment equipmentSnapshot,
	consumed []Item,
	gold, mileageMaterial uint64,
	identity, operation string,
) (Currency, uint64, error) {
	if identity == "" || mileageMaterial == 0 || s.wallet == nil || s.inventory == nil {
		return Currency{}, 0, errors.New("player: invalid transactional equipment smelting")
	}

	nextWallet := cloneWallet(s.wallet.state)
	if nextWallet.Spent[identity] {
		return Currency{}, 0, errors.New("player: equipment smelting request was already committed")
	}
	if nextWallet.Gold < gold {
		return Currency{}, 0, errors.New("player: insufficient gold for equipment smelting")
	}
	nextWallet.Gold -= gold
	nextWallet.Spent[identity] = true
	threshold, rewardCount := s.smelting.Mileage.UseCount, s.smelting.Mileage.RewardCount
	if threshold == 0 || rewardCount == 0 || nextWallet.EquipMileageExchangeGage >= threshold ||
		math.MaxUint64-nextWallet.EquipMileageExchangeGage < mileageMaterial {
		return Currency{}, 0, errors.New("player: invalid equipment smelting gauge")
	}
	total := nextWallet.EquipMileageExchangeGage + mileageMaterial
	exchanges := total / threshold
	earned := exchanges * rewardCount
	if exchanges != 0 && earned/exchanges != rewardCount || math.MaxUint64-nextWallet.EquipMileage < earned {
		return Currency{}, 0, errors.New("player: equipment mileage overflow")
	}
	nextWallet.EquipMileageExchangeGage = total % threshold
	nextWallet.EquipMileage += earned

	nextItems := cloneOwnedSnapshot(s.inventory.owned)
	for _, want := range consumed {
		if err := consumeOwnedItem(&nextItems, want); err != nil {
			return Currency{}, 0, err
		}
	}

	walletData, err := json.Marshal(nextWallet)
	if err != nil {
		return Currency{}, 0, err
	}
	if err := s.store.SaveWithEntries(ctx.State, "wallet", walletData, entry("spent", identity, []byte("true"))); err != nil {
		return Currency{}, 0, fmt.Errorf("player: persist equipment %s wallet: %w", operation, err)
	}
	if err := s.inventory.commitOwned(ctx, nextItems); err != nil {
		return Currency{}, 0, fmt.Errorf("player: persist equipment %s items: %w", operation, err)
	}
	if err := s.persistEquipment(ctx, nextEquipment, operation); err != nil {
		return Currency{}, 0, err
	}
	s.owned = nextEquipment
	s.persisted = cloneEquipmentSnapshot(nextEquipment)
	s.wallet.state = nextWallet
	s.inventory.owned = nextItems
	return nextWallet.Currency, earned, nil
}

func consumeOwnedItem(next *ownedSnapshot, want Item) error {
	if next == nil || want.InvenIndex == 0 || want.ID == 0 || want.Type == 0 || want.Count == 0 {
		return errors.New("player: invalid item consumption")
	}
	for i, current := range next.Items {
		if current.InvenIndex != want.InvenIndex {
			continue
		}
		if current.ID != want.ID || current.Type != want.Type || current.Count < want.Count {
			return fmt.Errorf("player: item %d consumption mismatch", want.InvenIndex)
		}
		current.Count -= want.Count
		if current.Count == 0 {
			next.Items = append(next.Items[:i], next.Items[i+1:]...)
		} else {
			next.Items[i] = current
		}
		return nil
	}
	return fmt.Errorf("player: item %d is not mutable-owned", want.InvenIndex)
}

func validateOptionRerollMaterials(costs []gamedata.PromotionCost, materials []Item, conversion *gamedata.EquipmentOptionRerollConversion) (uint64, []Item, error) {
	if len(costs) == 0 || len(materials) == 0 {
		return 0, nil, errors.New("player: EquipOptionReRoll has no material")
	}
	expected := make(map[[2]uint64]uint64, len(costs))
	for _, cost := range costs {
		key := [2]uint64{cost.Type, cost.ID}
		if cost.Type == 0 || cost.Count == 0 || (cost.Type == 4 && cost.ID != 0) || math.MaxUint64-expected[key] < cost.Count {
			return 0, nil, errors.New("player: invalid equipment option reroll cost")
		}
		expected[key] += cost.Count
	}
	actual := make(map[[2]uint64]uint64, len(materials))
	consumed := make([]Item, 0, len(materials))
	for _, material := range materials {
		key := [2]uint64{material.Type, material.ID}
		if math.MaxUint64-actual[key] < material.Count {
			return 0, nil, errors.New("player: EquipOptionReRoll material count overflows")
		}
		actual[key] += material.Count
		if material.Type != 4 {
			consumed = append(consumed, material)
		}
	}
	for key, want := range expected {
		if conversion != nil && key == ([2]uint64{conversion.TargetType, conversion.TargetID}) &&
			expected[[2]uint64{conversion.SourceType, conversion.SourceID}] == 0 {
			targetKey := key
			sourceKey := [2]uint64{conversion.SourceType, conversion.SourceID}
			targetCount := actual[targetKey]
			if targetCount > want || conversion.Ratio == 0 || want-targetCount > math.MaxUint64/conversion.Ratio || actual[sourceKey] != (want-targetCount)*conversion.Ratio {
				return 0, nil, fmt.Errorf("player: EquipOptionReRoll converted material %d/%d does not match cost", key[0], key[1])
			}
			delete(actual, targetKey)
			delete(actual, sourceKey)
			continue
		}
		if actual[key] != want {
			return 0, nil, fmt.Errorf("player: EquipOptionReRoll material %d/%d=%d want=%d", key[0], key[1], actual[key], want)
		}
		delete(actual, key)
	}
	if len(actual) != 0 {
		return 0, nil, errors.New("player: EquipOptionReRoll material kinds mismatch")
	}
	return expected[[2]uint64{4, 0}], consumed, nil
}

func (s *EquipmentInventory) commitOptionRerollLocked(ctx command.Context, pending *equipmentOptionRerollPending, consumed []Item, gold uint64, identity string) error {
	if pending == nil || identity == "" || s.wallet == nil || s.inventory == nil {
		return errors.New("player: invalid transactional equipment option reroll")
	}

	nextWallet := cloneWallet(s.wallet.state)
	if nextWallet.Spent[identity] {
		return errors.New("player: equipment option reroll request was already committed")
	}
	if nextWallet.Gold < gold {
		return errors.New("player: insufficient gold for equipment option reroll")
	}
	nextWallet.Gold -= gold
	nextWallet.Spent[identity] = true
	nextItems := cloneOwnedSnapshot(s.inventory.owned)
	for _, want := range consumed {
		if err := consumeOwnedItem(&nextItems, want); err != nil {
			return err
		}
	}
	walletData, err := json.Marshal(nextWallet)
	if err != nil {
		return err
	}
	if err := s.store.SaveWithEntries(ctx.State, "wallet", walletData, entry("spent", identity, []byte("true"))); err != nil {
		return fmt.Errorf("player: persist equipment option reroll wallet: %w", err)
	}
	if err := s.inventory.commitOwned(ctx, nextItems); err != nil {
		return fmt.Errorf("player: persist equipment option reroll items: %w", err)
	}
	if err := s.persistEquipmentState(ctx, s.owned, pending, true, "option reroll"); err != nil {
		return err
	}
	s.wallet.state = nextWallet
	s.inventory.owned = nextItems
	s.pendingReroll = &equipmentOptionRerollPending{Equipment: cloneEquipment(pending.Equipment)}
	return nil
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

func (s *EquipmentInventory) selectUpgradeCosts(ctx command.Context, costs []gamedata.PromotionCost) ([]Item, uint64, error) {
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
			items, err := s.inventory.SelectMutable(ctx, cost.Type, cost.ID, cost.Count)
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

func (s *EquipmentInventory) attemptUpgradeLocked(ctx command.Context, index uint64, materials []Item) (Equipment, bool, uint64, []Item, error) {
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
		if err := s.inventory.CanConsume(ctx, items); err != nil {
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
		if _, err := s.wallet.SpendGoldOnce(ctx, identity, gold); err != nil {
			return Equipment{}, false, 0, nil, fmt.Errorf("player: spend equipment upgrade gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(ctx, items); err != nil {
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
	if err := s.commitLocked(ctx, next, "upgrade"); err != nil {
		return Equipment{}, false, 0, nil, err
	}
	return next.Equipment[position], success, gold, materials, nil
}

func (s *EquipmentInventory) equippedCharacter(ctx command.Context, entry Equipment) (EquipmentCharacter, bool) {
	if entry.UseChar == 0 || s.characters == nil {
		return EquipmentCharacter{}, false
	}
	return s.characters.EquipmentCharacter(ctx, entry.UseChar)
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
	if suffix, found := strings.CutPrefix(left, "~"); found {
		icon, err := strconv.Atoi(suffix)
		return err == nil && icon >= 1 && icon <= 15
	}
	if text, found := strings.CutPrefix(left, "_"); found {
		return utf8.RuneCountInString(text) == 1 && len([]byte(text)) <= 2
	}
	return len(left) >= 1 && len([]byte(left)) <= 2
}

func cloneEquipmentSnapshot(current equipmentSnapshot) equipmentSnapshot {
	next := equipmentSnapshot{Version: current.Version, NextIndex: current.NextIndex,
		Equipment: append([]Equipment(nil), current.Equipment...), Granted: make(map[string]uint64, len(current.Granted))}
	for i := range next.Equipment {
		next.Equipment[i] = cloneEquipment(next.Equipment[i])
	}
	maps.Copy(next.Granted, current.Granted)
	return next
}

func cloneEquipment(current Equipment) Equipment {
	next := current
	next.MainOption = append([]EquipmentOption(nil), current.MainOption...)
	next.SubOption = append([]EquipmentOption(nil), current.SubOption...)
	next.Rank = append([]uint64(nil), current.Rank...)
	if current.PrivateOption != nil {
		option := *current.PrivateOption
		next.PrivateOption = &option
	}
	return next
}

func (s *EquipmentInventory) validatePendingRerollLocked(pending *equipmentOptionRerollPending) error {
	if pending == nil || pending.Equipment.InvenIndex == 0 || pending.Equipment.ID == 0 {
		return errors.New("player: invalid equipment option reroll candidate")
	}
	position := s.equipmentPositionLocked(pending.Equipment.InvenIndex)
	if position < 0 {
		return fmt.Errorf("player: option reroll candidate references unknown equipment %d", pending.Equipment.InvenIndex)
	}
	current := s.owned.Equipment[position]
	if len(pending.Equipment.MainOption) != len(current.MainOption) || len(pending.Equipment.SubOption) != len(current.SubOption) {
		return errors.New("player: option reroll candidate option counts do not match equipment")
	}
	for i, option := range pending.Equipment.MainOption {
		if option.GroupID != current.MainOption[i].GroupID || option.ID == 0 {
			return fmt.Errorf("player: option reroll candidate main option %d is invalid", i)
		}
	}
	for i, option := range pending.Equipment.SubOption {
		if option.GroupID != current.SubOption[i].GroupID || option.ID == 0 {
			return fmt.Errorf("player: option reroll candidate sub option %d is invalid", i)
		}
	}
	if s.optionReroll != nil {
		definition, ok := s.optionReroll.Lookup(current.ID)
		if !ok || len(definition.MainGroups) != len(pending.Equipment.MainOption) || len(definition.SubGroups) != len(pending.Equipment.SubOption) {
			return errors.New("player: option reroll candidate has no matching GameData design")
		}
		for i, option := range pending.Equipment.MainOption {
			if option.GroupID != definition.MainGroups[i] || !optionChoiceExists(s.optionReroll.Groups[option.GroupID], option.ID) {
				return fmt.Errorf("player: option reroll candidate main option %d is not in GameData", i)
			}
		}
		for i, option := range pending.Equipment.SubOption {
			if option.GroupID != definition.SubGroups[i] || !optionChoiceExists(s.optionReroll.Groups[option.GroupID], option.ID) {
				return fmt.Errorf("player: option reroll candidate sub option %d is not in GameData", i)
			}
		}
	}
	invariant := cloneEquipment(pending.Equipment)
	invariant.MainOption = append([]EquipmentOption(nil), current.MainOption...)
	invariant.SubOption = append([]EquipmentOption(nil), current.SubOption...)
	if !reflect.DeepEqual(invariant, current) {
		return errors.New("player: option reroll candidate modifies immutable equipment state")
	}
	return nil
}

func optionChoiceExists(group gamedata.OptionGroup, id uint64) bool {
	for _, choice := range group.Choices {
		if choice.ID == id {
			return true
		}
	}
	return false
}

func (s *EquipmentInventory) commitLocked(ctx command.Context, next equipmentSnapshot, operation string) error {
	if err := s.persistEquipment(ctx, next, operation); err != nil {
		return err
	}
	s.owned = next
	s.persisted = cloneEquipmentSnapshot(next)
	return nil
}

func (s *EquipmentInventory) persistEquipment(ctx command.Context, next equipmentSnapshot, operation string) error {
	return s.persistEquipmentState(ctx, next, nil, false, operation)
}

func (s *EquipmentInventory) persistEquipmentState(ctx command.Context, next equipmentSnapshot, pending *equipmentOptionRerollPending, pendingDirty bool, operation string) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	changes := make([]stateio.EntryMutation, 0)
	for key, value := range next.Granted {
		if value != 0 && s.persisted.Granted[key] != value {
			changes = append(changes, stateio.EntryMutation{Bucket: "granted", Key: key, Payload: []byte(strconv.FormatUint(value, 10))})
		}
	}
	before := make(map[uint64]Equipment, len(s.persisted.Equipment))
	for _, item := range s.persisted.Equipment {
		before[item.InvenIndex] = item
	}
	for _, item := range next.Equipment {
		old, exists := before[item.InvenIndex]
		if !exists || !reflect.DeepEqual(old, item) {
			payload, err := json.Marshal(item)
			if err != nil {
				return err
			}
			changes = append(changes, stateio.EntryMutation{Bucket: "equipment", Key: strconv.FormatUint(item.InvenIndex, 10), Payload: payload})
		}
		delete(before, item.InvenIndex)
	}
	for index := range before {
		changes = append(changes, stateio.EntryMutation{Bucket: "equipment", Key: strconv.FormatUint(index, 10), Delete: true})
	}
	if pendingDirty {
		change := stateio.EntryMutation{Bucket: "reroll_pending", Key: "current", Delete: pending == nil}
		if pending != nil {
			change.Payload, err = json.Marshal(pending)
			if err != nil {
				return err
			}
		}
		changes = append(changes, change)
	}
	if s.corePresent && next.Version == s.persisted.Version && next.NextIndex == s.persisted.NextIndex {
		data = nil
	}
	if err := s.store.SaveWithEntries(ctx.State, "equipment", data, changes); err != nil {
		return fmt.Errorf("player: persist equipment %s: %w", operation, err)
	}
	s.corePresent = true
	return nil
}

func (s *EquipmentInventory) All(ctx command.Context) []Equipment {

	return append([]Equipment(nil), s.owned.Equipment...)
}

// Granted returns a prior idempotent grant without creating an item.
func (s *EquipmentInventory) Granted(identity string) (Equipment, bool) {

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
