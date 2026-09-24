package player

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"bd2server/internal/gamedata"
	"bd2server/internal/stateio"
	"bd2server/internal/versionconfig"
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
	Equipment []Equipment       `json:"-"`
	Granted   map[string]uint64 `json:"-"`
}

// EquipmentInventory persists non-stackable equipment independently from
// ItemDBInfo inventory because the wire protocols are different types.
type EquipmentInventory struct {
	mu            sync.Mutex
	store         stateio.AtomicEntryStore
	owned         equipmentSnapshot
	persisted     equipmentSnapshot
	corePresent   bool
	characters    *CharacterStore
	slots         map[uint64]uint64
	upgrade       *gamedata.EquipmentUpgradeDesign
	smelting      *gamedata.EquipmentSmeltingDesign
	optionReroll  *gamedata.EquipmentOptionRerollDesign
	wallet        *Wallet
	inventory     *Inventory
	sessionID     string
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

func (s *EquipmentInventory) AttachUpgrade(design *gamedata.EquipmentUpgradeDesign, wallet *Wallet, inventory *Inventory) error {
	if design == nil || wallet == nil || inventory == nil {
		return errors.New("player: incomplete equipment upgrade configuration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upgrade, s.wallet, s.inventory = design, wallet, inventory
	return nil
}

func (s *EquipmentInventory) AttachSmelting(design *gamedata.EquipmentSmeltingDesign, wallet *Wallet, inventory *Inventory) error {
	if design == nil || wallet == nil || inventory == nil {
		return errors.New("player: incomplete equipment smelting configuration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.smelting, s.wallet, s.inventory = design, wallet, inventory
	return nil
}

func (s *EquipmentInventory) AttachOptionReroll(design *gamedata.EquipmentOptionRerollDesign, wallet *Wallet, inventory *Inventory) error {
	if design == nil || wallet == nil || inventory == nil {
		return errors.New("player: incomplete equipment option reroll configuration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.optionReroll, s.wallet, s.inventory = design, wallet, inventory
	if s.pendingReroll != nil {
		if err := s.validatePendingRerollLocked(s.pendingReroll); err != nil {
			return err
		}
	}
	return nil
}

// BeginSession scopes the in-memory request replay cache. A repeated protobuf
// sequence in one login must return the first refinement result without a
// second roll or charge.
func (s *EquipmentInventory) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = id
	s.smeltCache = make(map[string]smeltingReply)
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
	known := make(map[uint64]bool)
	for _, character := range characters.RawAll() {
		known[character.InvenIndex] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.presets {
		if !known[key.CharacterIndex] {
			return fmt.Errorf("player: equipment preset references unknown character %d", key.CharacterIndex)
		}
	}
	s.characters = characters
	return nil
}

func OpenEquipmentInventory(store stateio.Store) (*EquipmentInventory, error) {
	entries, ok := store.(stateio.AtomicEntryStore)
	if !ok {
		return nil, errors.New("player: nil equipment store")
	}
	s := &EquipmentInventory{store: entries, smeltCache: make(map[string]smeltingReply), presets: make(map[equipmentPresetKey]equipmentPreset), owned: equipmentSnapshot{
		Version: versionconfig.Protocol(), NextIndex: 910000001, Granted: map[string]uint64{},
	}}
	data, err := store.Load("equipment")
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
	} else if err := stateio.RequireNoEntries(entries, "equipment", "equipment", "granted", "reroll_pending", "presets"); err != nil {
		return nil, fmt.Errorf("player: invalid equipment storage: %w", err)
	}
	if s.owned.Version != versionconfig.Protocol() || s.owned.NextIndex < 910000001 {
		return nil, errors.New("player: invalid saved equipment")
	}
	s.owned.Granted, err = loadUintEntries(entries, "equipment", "granted")
	if err != nil {
		return nil, err
	}
	rawEquipment, err := entries.ListEntries("equipment", "equipment")
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
	pendingEntries, err := entries.ListEntries("equipment", "reroll_pending")
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
	if err := s.loadEquipmentPresets(entries); err != nil {
		return nil, err
	}
	s.persisted = cloneEquipmentSnapshot(s.owned)
	return s, nil
}

func (s *EquipmentInventory) EnsurePersisted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.store.Load("equipment")
	if err != nil {
		return err
	}
	if data != nil {
		return nil
	}
	return s.commitLocked(cloneEquipmentSnapshot(s.owned), "initial account generation")
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
	if err := s.commitLocked(next, "grant"); err != nil {
		return Equipment{}, err
	}
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
	if path != "/EquipInfo" && path != "/EquipUse" && path != "/EquipClear" && path != "/EquipChange" && path != "/EquipBatchUse" && path != "/EquipPresetInfo" && path != "/EquipPresetSave" && path != "/EquipPresetNameChange" && path != "/EquipUpgrade" && path != "/EquipSequenceUpgrade" && path != "/EquipSmelting" && path != "/EquipSequenceSmelting" && path != "/EquipOptionReRoll" && path != "/EquipOptionReRollConfirm" && path != "/EquipMainOptChange" && path != "/EquipMarkSet" && path != "/EquipMarkDelete" && path != "/EquipLock" {
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
	if path == "/EquipBatchUse" {
		return s.batchUse(request)
	}
	if path == "/EquipPresetInfo" {
		return s.presetInfo()
	}
	if path == "/EquipPresetSave" {
		return s.presetSave(request)
	}
	if path == "/EquipPresetNameChange" {
		return s.presetNameChange(request)
	}
	if path == "/EquipUpgrade" {
		return s.upgradeOnce(request)
	}
	if path == "/EquipSequenceUpgrade" {
		return s.upgradeSequence(request)
	}
	if path == "/EquipSmelting" {
		return s.smeltOnce(request)
	}
	if path == "/EquipSequenceSmelting" {
		return s.smeltSequence(request)
	}
	if path == "/EquipOptionReRoll" {
		return s.optionRerollRequest(request)
	}
	if path == "/EquipOptionReRollConfirm" {
		return s.optionRerollConfirm(request)
	}
	if path == "/EquipMainOptChange" {
		return s.mainOptionChange(request)
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
	if s.pendingReroll != nil {
		response = wire.AppendBytes(response, 2, EquipmentWire(s.pendingReroll.Equipment))
	}
	return 34, response, true, nil
}

func (s *EquipmentInventory) optionRerollRequest(request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipOptionReRoll missing equipment")
	}
	mainLocks, err := repeatedBoolField(request, 3, "EquipOptionReRoll main lock")
	if err != nil {
		return 0, nil, true, err
	}
	subLocks, err := repeatedBoolField(request, 4, "EquipOptionReRoll sub lock")
	if err != nil {
		return 0, nil, true, err
	}
	materials, err := equipmentRequestItems(request, 5, "EquipOptionReRoll")
	if err != nil {
		return 0, nil, true, err
	}
	rerollType, _, err := wire.Varint(request, 6)
	if err != nil || rerollType > 1 {
		return 0, nil, true, errors.New("player: EquipOptionReRoll unsupported reroll type")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cacheKey := s.smeltingCacheKey("option-reroll", seq)
	if reply, ok := s.smeltCache[cacheKey]; ok {
		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	if s.optionReroll == nil || s.wallet == nil || s.inventory == nil {
		return 0, nil, true, errors.New("player: equipment option reroll unavailable")
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipOptionReRoll unknown equipment %d", index)
	}
	current := s.owned.Equipment[position]
	base := current
	if s.pendingReroll != nil {
		if s.pendingReroll.Equipment.InvenIndex != index {
			return 0, nil, true, errors.New("player: another equipment option reroll is awaiting confirmation")
		}
		// A retry from the result screen carries only lock masks. The values
		// being locked are therefore the last candidate, not the still-official
		// equipment options. Keep chaining candidates until confirm/keep clears
		// the pending result.
		base = s.pendingReroll.Equipment
	}
	definition, ok := s.optionReroll.Lookup(current.ID)
	if !ok {
		return 0, nil, true, fmt.Errorf("player: equipment %d has no option reroll design", current.ID)
	}
	if len(mainLocks) != len(base.MainOption) || len(mainLocks) != len(definition.MainGroups) ||
		len(subLocks) != len(base.SubOption) || len(subLocks) != len(definition.SubGroups) {
		return 0, nil, true, errors.New("player: EquipOptionReRoll lock arrays do not match equipment options")
	}
	effectiveMainLocks := append([]bool(nil), mainLocks...)
	if rerollType == 1 && len(effectiveMainLocks) != 0 {
		// The client's has-another-option mode asks the server to leave the
		// first main option alone. It is not a paid lock and remains false in
		// the request mask.
		effectiveMainLocks[0] = true
	}
	lockedCount, unlockedRerollable := uint64(0), uint64(0)
	for i, locked := range mainLocks {
		if base.MainOption[i].GroupID != definition.MainGroups[i] || base.MainOption[i].ID == 0 {
			return 0, nil, true, fmt.Errorf("player: equipment %d main option %d does not match GameData", index, i)
		}
		canReroll := len(s.optionReroll.Groups[definition.MainGroups[i]].Choices) >= 2
		if locked && (i == 0 || !canReroll) {
			return 0, nil, true, fmt.Errorf("player: EquipOptionReRoll main option %d cannot be locked", i)
		}
		if locked {
			lockedCount++
		} else if canReroll && !(rerollType == 1 && i == 0) {
			unlockedRerollable++
		}
	}
	for i, locked := range subLocks {
		if base.SubOption[i].GroupID != definition.SubGroups[i] || base.SubOption[i].ID == 0 {
			return 0, nil, true, fmt.Errorf("player: equipment %d sub option %d does not match GameData", index, i)
		}
		canReroll := len(s.optionReroll.Groups[definition.SubGroups[i]].Choices) >= 2
		if locked && !canReroll {
			return 0, nil, true, fmt.Errorf("player: EquipOptionReRoll sub option %d cannot be locked", i)
		}
		if locked {
			lockedCount++
		} else if canReroll {
			unlockedRerollable++
		}
	}
	if unlockedRerollable == 0 {
		return 0, nil, true, errors.New("player: EquipOptionReRoll must leave a rerollable option unlocked")
	}
	costs, err := s.optionReroll.Cost(current.ID, lockedCount)
	if err != nil {
		return 0, nil, true, err
	}
	gold, consumed, err := validateOptionRerollMaterials(costs, materials, s.optionReroll.Conversion)
	if err != nil {
		return 0, nil, true, err
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for equipment option reroll")
	}
	if len(consumed) != 0 {
		if err := s.inventory.CanConsume(consumed); err != nil {
			return 0, nil, true, err
		}
	}
	privateLocks := make([]bool, len(definition.PrivateGroups))
	for i := range privateLocks {
		privateLocks[i] = true
	}
	rolled, err := s.optionReroll.RollUnlocked(current.ID, gamedata.EquipmentOptionRerollLocks{Main: effectiveMainLocks, Sub: subLocks, Private: privateLocks})
	if err != nil {
		return 0, nil, true, err
	}
	candidate := cloneEquipment(base)
	for i := range candidate.MainOption {
		if !effectiveMainLocks[i] {
			candidate.MainOption[i] = EquipmentOption{GroupID: rolled.Main[i].GroupID, ID: rolled.Main[i].ID}
		}
	}
	for i := range candidate.SubOption {
		if !subLocks[i] {
			candidate.SubOption[i] = EquipmentOption{GroupID: rolled.Sub[i].GroupID, ID: rolled.Sub[i].ID}
		}
	}
	pending := &equipmentOptionRerollPending{Equipment: candidate}
	if err := s.commitOptionRerollLocked(pending, consumed, gold, "equip-option-reroll:"+cacheKey); err != nil {
		return 0, nil, true, err
	}
	var response []byte
	for _, option := range candidate.MainOption {
		response = wire.AppendBytes(response, 1, equipmentOptionWire(option))
	}
	for _, option := range candidate.SubOption {
		response = wire.AppendBytes(response, 2, equipmentOptionWire(option))
	}
	s.smeltCache[cacheKey] = smeltingReply{code: 192, body: append([]byte(nil), response...)}
	return 192, response, true, nil
}

func (s *EquipmentInventory) optionRerollConfirm(request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipOptionReRollConfirm missing equipment")
	}
	confirm, err := optionalBoolField(request, 3, "EquipOptionReRollConfirm confirm")
	if err != nil {
		return 0, nil, true, err
	}
	s.mu.Lock()
	cacheKey := s.smeltingCacheKey("option-reroll-confirm", seq)
	if reply, ok := s.smeltCache[cacheKey]; ok {
		s.mu.Unlock()
		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	if s.pendingReroll == nil || s.pendingReroll.Equipment.InvenIndex != index {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: equipment %d has no option reroll awaiting confirmation", index)
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {
		s.mu.Unlock()
		return 0, nil, true, fmt.Errorf("player: EquipOptionReRollConfirm unknown equipment %d", index)
	}
	next := cloneEquipmentSnapshot(s.owned)
	if confirm {
		next.Equipment[position].MainOption = append([]EquipmentOption(nil), s.pendingReroll.Equipment.MainOption...)
		next.Equipment[position].SubOption = append([]EquipmentOption(nil), s.pendingReroll.Equipment.SubOption...)
	}
	if err := s.persistEquipmentState(next, nil, true, "option reroll confirm"); err != nil {
		s.mu.Unlock()
		return 0, nil, true, err
	}
	s.owned = next
	s.persisted = cloneEquipmentSnapshot(next)
	s.pendingReroll = nil
	entry := cloneEquipment(next.Equipment[position])
	s.mu.Unlock()
	response := wire.AppendBytes(nil, 1, EquipmentWire(entry))
	if character, ok := s.equippedCharacter(entry); ok {
		response = wire.AppendBytes(response, 2, CharacterWire(character))
	}
	s.mu.Lock()
	s.smeltCache[cacheKey] = smeltingReply{code: 193, body: append([]byte(nil), response...)}
	s.mu.Unlock()
	return 193, response, true, nil
}

func (s *EquipmentInventory) mainOptionChange(request []byte) (int, []byte, bool, error) {
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipMainOptChange missing equipment")
	}
	groupID, groupFound, err := wire.Varint(request, 3)
	if err != nil || !groupFound || groupID == 0 {
		return 0, nil, true, errors.New("player: EquipMainOptChange missing option group")
	}
	optionID, optionFound, err := wire.Varint(request, 4)
	if err != nil || !optionFound || optionID == 0 {
		return 0, nil, true, errors.New("player: EquipMainOptChange missing option")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.optionReroll == nil {
		return 0, nil, true, errors.New("player: equipment option design unavailable")
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipMainOptChange unknown equipment %d", index)
	}
	current := s.owned.Equipment[position]
	definition, ok := s.optionReroll.Lookup(current.ID)
	if !ok || definition.PrivateUniqueCharID == 0 || len(definition.MainGroups) == 0 || len(current.MainOption) == 0 {
		return 0, nil, true, fmt.Errorf("player: equipment %d has no changeable main option", current.ID)
	}
	group, ok := s.optionReroll.Groups[groupID]
	if !ok || groupID != definition.MainGroups[0] || current.MainOption[0].GroupID != groupID || len(group.Choices) < 2 || !optionChoiceExists(group, optionID) {
		return 0, nil, true, fmt.Errorf("player: equipment %d main option %d/%d is not allowed", current.ID, groupID, optionID)
	}

	next := cloneEquipmentSnapshot(s.owned)
	next.Equipment[position].MainOption[0] = EquipmentOption{GroupID: groupID, ID: optionID}
	pending := s.pendingReroll
	pendingDirty := false
	if pending != nil && pending.Equipment.InvenIndex == index {
		copy := &equipmentOptionRerollPending{Equipment: cloneEquipment(pending.Equipment)}
		if len(copy.Equipment.MainOption) == 0 || copy.Equipment.MainOption[0].GroupID != groupID {
			return 0, nil, true, errors.New("player: option reroll candidate has no matching main option")
		}
		copy.Equipment.MainOption[0] = EquipmentOption{GroupID: groupID, ID: optionID}
		pending = copy
		pendingDirty = true
	}
	if err := s.persistEquipmentState(next, pending, pendingDirty, "main option change"); err != nil {
		return 0, nil, true, err
	}
	s.owned = next
	s.persisted = cloneEquipmentSnapshot(next)
	if pendingDirty {
		s.pendingReroll = pending
	}
	var response []byte
	if character, ok := s.equippedCharacter(current); ok {
		response = wire.AppendBytes(response, 1, CharacterWire(character))
	}
	return 537, response, true, nil
}

func repeatedBoolField(data []byte, number int, name string) ([]bool, error) {
	var result []bool
	err := wire.Walk(data, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		switch field.Type {
		case 0:
			value, count := binary.Uvarint(field.Value)
			if count <= 0 || value > 1 {
				return fmt.Errorf("player: %s is invalid", name)
			}
			result = append(result, value == 1)
		case 2:
			for offset := 0; offset < len(field.Value); {
				value, count := binary.Uvarint(field.Value[offset:])
				if count <= 0 || value > 1 {
					return fmt.Errorf("player: %s is invalid", name)
				}
				result = append(result, value == 1)
				offset += count
			}
		default:
			return fmt.Errorf("player: %s is invalid", name)
		}
		return nil
	})
	return result, err
}

func optionalBoolField(data []byte, number int, name string) (bool, error) {
	value := false
	seen := false
	err := wire.Walk(data, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		if seen || field.Type != 0 {
			return fmt.Errorf("player: %s is invalid", name)
		}
		raw, count := binary.Uvarint(field.Value)
		if count <= 0 || raw > 1 {
			return fmt.Errorf("player: %s is invalid", name)
		}
		seen = true
		value = raw == 1
		return nil
	})
	return value, err
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
	return 176, response, true, nil
}

func (s *EquipmentInventory) smeltOnce(request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipSmelting missing equipment")
	}
	materials, err := equipmentRequestItems(request, 3, "EquipSmelting")
	if err != nil {
		return 0, nil, true, err
	}
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	cacheKey := s.smeltingCacheKey("single", seq)
	if reply, ok := s.smeltCache[cacheKey]; ok {
		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	position, current, err := s.smeltingEquipmentLocked(index)
	if err != nil {
		return 0, nil, true, err
	}
	costs, err := s.smelting.Cost(current.ID)
	if err != nil {
		return 0, nil, true, err
	}
	gold, _, err := validateSmeltingMaterials(costs, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for equipment smelting")
	}
	var itemMaterials []Item
	var consumedMileageMaterial uint64
	for _, item := range materials {
		if item.Type != 4 {
			itemMaterials = append(itemMaterials, item)
		}
		if item.Type == s.smelting.Mileage.UseType && item.ID == s.smelting.Mileage.UseID {
			consumedMileageMaterial += item.Count
		}
	}
	if len(itemMaterials) != 0 {
		if err := s.inventory.CanConsume(itemMaterials); err != nil {
			return 0, nil, true, err
		}
	}
	candidate, err := s.smelting.RollCandidate(current.ID)
	if err != nil {
		return 0, nil, true, err
	}
	currentScore, err := s.smelting.Score(current.ID, current.Rank)
	if err != nil {
		return 0, nil, true, err
	}
	candidateScore, err := s.smelting.Score(current.ID, candidate)
	if err != nil {
		return 0, nil, true, err
	}
	success := candidateScore > currentScore
	next := cloneEquipmentSnapshot(s.owned)
	if success {
		next.Equipment[position].Rank = append([]uint64(nil), candidate...)
	}
	currency, earned, err := s.commitSmeltingLocked(
		next, itemMaterials, gold, consumedMileageMaterial,
		"equip-smelting:"+cacheKey, "smelting")
	if err != nil {
		return 0, nil, true, err
	}
	current = next.Equipment[position]
	response := wire.AppendBytes(nil, 1, EquipmentWire(current))
	s.mu.Unlock()
	locked = false
	if character, ok := s.equippedCharacter(current); ok {
		response = wire.AppendBytes(response, 2, CharacterWire(character))
	}
	if !success {
		response = wire.AppendVarint(response, 3, equipUpgradeFail)
		for _, rank := range candidate {
			response = wire.AppendVarint(response, 4, rank)
		}
	}
	response = appendSmeltingMileage(response, 5, 6, currency.EquipMileageExchangeGage, s.smelting.Mileage, earned)
	s.mu.Lock()
	s.smeltCache[cacheKey] = smeltingReply{code: 105, body: append([]byte(nil), response...)}
	s.mu.Unlock()
	return 105, response, true, nil
}

func (s *EquipmentInventory) smeltSequence(request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipSequenceSmelting missing equipment")
	}
	count, found, err := wire.Varint(request, 3)
	if err != nil || !found || count == 0 {
		return 0, nil, true, errors.New("player: EquipSequenceSmelting invalid attempt count")
	}
	target, _, err := wire.Varint(request, 4)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipSequenceSmelting invalid target score")
	}
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	cacheKey := s.smeltingCacheKey("sequence", seq)
	if reply, ok := s.smeltCache[cacheKey]; ok {
		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	position, current, err := s.smeltingEquipmentLocked(index)
	if err != nil {
		return 0, nil, true, err
	}
	if count > s.smelting.MaxStreak {
		return 0, nil, true, fmt.Errorf("player: EquipSequenceSmelting attempt count %d exceeds %d", count, s.smelting.MaxStreak)
	}
	maximumRanks, err := s.smelting.MaximumRanks(current.ID)
	if err != nil {
		return 0, nil, true, err
	}
	maximumScore, err := s.smelting.Score(current.ID, maximumRanks)
	if err != nil {
		return 0, nil, true, err
	}
	if target > maximumScore {
		return 0, nil, true, fmt.Errorf("player: EquipSequenceSmelting target %d exceeds %d", target, maximumScore)
	}
	costs, err := s.smelting.Cost(current.ID)
	if err != nil {
		return 0, nil, true, err
	}
	currentScore, err := s.smelting.Score(current.ID, current.Rank)
	if err != nil {
		return 0, nil, true, err
	}
	// The 2.35.10 client splits a requested sequence into packets of at most
	// 1000 attempts. Exhausting this packet is not the user's global max-try
	// stop: UPGRADE_SUCCESS tells the client to send the next chunk. Terminal
	// stop values are reserved for target/max score and insufficient resources.
	result := uint64(equipUpgradeSuccess)
	var attempts, successes uint64
	if currentScore >= maximumScore {
		result = equipUpgradeStopMaxLevel
	} else if target != 0 && currentScore >= target {
		result = equipUpgradeStopTargetLevel
	}
	for attempts < count && result == equipUpgradeSuccess {
		if _, _, selectErr := s.selectSmeltingCosts(costs, attempts+1); selectErr != nil {
			result = equipUpgradeStopNotEnough
			break
		}
		candidate, rollErr := s.smelting.RollCandidate(current.ID)
		if rollErr != nil {
			return 0, nil, true, rollErr
		}
		candidateScore, scoreErr := s.smelting.Score(current.ID, candidate)
		if scoreErr != nil {
			return 0, nil, true, scoreErr
		}
		attempts++
		if candidateScore > currentScore {
			current.Rank = append([]uint64(nil), candidate...)
			currentScore = candidateScore
			successes++
		}
		if currentScore >= maximumScore {
			result = equipUpgradeStopMaxLevel
		} else if target != 0 && currentScore >= target {
			result = equipUpgradeStopTargetLevel
		}
	}
	var consumed, lack []Item
	var gold, mileageMaterial, earned uint64
	currency := s.wallet.Snapshot()
	if attempts != 0 {
		consumed, gold, err = s.selectSmeltingCosts(costs, attempts)
		if err != nil {
			return 0, nil, true, err
		}
		for _, item := range consumed {
			if item.Type == s.smelting.Mileage.UseType && item.ID == s.smelting.Mileage.UseID {
				mileageMaterial += item.Count
			}
		}
		var itemMaterials []Item
		for _, item := range consumed {
			if item.Type != 4 {
				itemMaterials = append(itemMaterials, item)
			}
		}
		next := cloneEquipmentSnapshot(s.owned)
		next.Equipment[position].Rank = append([]uint64(nil), current.Rank...)
		currency, earned, err = s.commitSmeltingLocked(
			next, itemMaterials, gold, mileageMaterial,
			"equip-sequence-smelting:"+cacheKey, "sequence smelting")
		if err != nil {
			return 0, nil, true, err
		}
		current = next.Equipment[position]
	}
	// NotEnough is used only when the next requested attempt could not be
	// funded. Exhausting this packet retains Success so the client can continue
	// a sequence whose total requested count exceeds the 1000-attempt chunk.
	if result == equipUpgradeStopNotEnough {
		lack = upgradeLackItems(costs)
	}
	response := wire.AppendBytes(nil, 1, EquipmentWire(current))
	s.mu.Unlock()
	locked = false
	if character, ok := s.equippedCharacter(current); ok {
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
	response = appendSmeltingMileage(response, 7, 8, currency.EquipMileageExchangeGage, s.smelting.Mileage, earned)
	if successes != 0 {
		response = wire.AppendVarint(response, 9, successes)
	}
	s.mu.Lock()
	s.smeltCache[cacheKey] = smeltingReply{code: 177, body: append([]byte(nil), response...)}
	s.mu.Unlock()
	return 177, response, true, nil
}

func (s *EquipmentInventory) smeltingCacheKey(kind string, seq uint64) string {
	return kind + ":" + s.sessionID + ":seq:" + strconv.FormatUint(seq, 10)
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

func equipmentRequestItems(request []byte, number int, operation string) ([]Item, error) {
	var result []Item
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		if field.Type != 2 {
			return fmt.Errorf("player: %s invalid material", operation)
		}
		var item Item
		if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 9: &item.SortID, 10: &item.UseCount}); err != nil {
			return err
		}
		if item.Type == 0 || item.Count == 0 || (item.Type == 4 && (item.ID != 0 || item.InvenIndex != 0)) || (item.Type != 4 && (item.ID == 0 || item.InvenIndex == 0)) {
			return fmt.Errorf("player: %s invalid material", operation)
		}
		result = append(result, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("player: %s has no material", operation)
	}
	return result, nil
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

func (s *EquipmentInventory) selectSmeltingCosts(costs []gamedata.PromotionCost, attempts uint64) ([]Item, uint64, error) {
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
			items, err := s.inventory.SelectMutable(cost.Type, cost.ID, count)
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

func appendSmeltingMileage(response []byte, gaugeField, rewardField int, gauge uint64, mileage gamedata.EquipmentSmeltingMileage, earned uint64) []byte {
	if gauge != 0 {
		response = wire.AppendVarint(response, gaugeField, gauge)
	}
	if earned != 0 {
		reward := ItemWire(Item{ID: mileage.RewardID, Type: mileage.RewardType, Count: earned})
		bundle := wire.AppendBytes(nil, 1, reward)
		response = wire.AppendBytes(response, rewardField, bundle)
	}
	return response
}

// commitSmeltingLocked keeps refinement's three typed snapshots synchronized
// in memory. Caller holds equipment.mu; this method takes the remaining locks
// in wallet -> inventory order, calculates every candidate before writing,
// then publishes all three only after their saves succeed. The surrounding
// request transaction supplies durable all-or-none recovery across the writes.
func (s *EquipmentInventory) commitSmeltingLocked(
	nextEquipment equipmentSnapshot,
	consumed []Item,
	gold, mileageMaterial uint64,
	identity, operation string,
) (Currency, uint64, error) {
	if identity == "" || mileageMaterial == 0 || s.wallet == nil || s.inventory == nil {
		return Currency{}, 0, errors.New("player: invalid transactional equipment smelting")
	}
	s.wallet.mu.Lock()
	defer s.wallet.mu.Unlock()
	s.inventory.mu.Lock()
	defer s.inventory.mu.Unlock()

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
	if err := s.store.SaveWithEntries("wallet", walletData, entry("spent", identity, []byte("true"))); err != nil {
		return Currency{}, 0, fmt.Errorf("player: persist equipment %s wallet: %w", operation, err)
	}
	if err := s.inventory.commitOwned(nextItems); err != nil {
		return Currency{}, 0, fmt.Errorf("player: persist equipment %s items: %w", operation, err)
	}
	if err := s.persistEquipment(nextEquipment, operation); err != nil {
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

func (s *EquipmentInventory) commitOptionRerollLocked(pending *equipmentOptionRerollPending, consumed []Item, gold uint64, identity string) error {
	if pending == nil || identity == "" || s.wallet == nil || s.inventory == nil {
		return errors.New("player: invalid transactional equipment option reroll")
	}
	s.wallet.mu.Lock()
	defer s.wallet.mu.Unlock()
	s.inventory.mu.Lock()
	defer s.inventory.mu.Unlock()

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
	if err := s.store.SaveWithEntries("wallet", walletData, entry("spent", identity, []byte("true"))); err != nil {
		return fmt.Errorf("player: persist equipment option reroll wallet: %w", err)
	}
	if err := s.inventory.commitOwned(nextItems); err != nil {
		return fmt.Errorf("player: persist equipment option reroll items: %w", err)
	}
	if err := s.persistEquipmentState(s.owned, pending, true, "option reroll"); err != nil {
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
	for i := range next.Equipment {
		next.Equipment[i] = cloneEquipment(next.Equipment[i])
	}
	for key, value := range current.Granted {
		next.Granted[key] = value
	}
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

func (s *EquipmentInventory) commitLocked(next equipmentSnapshot, operation string) error {
	if err := s.persistEquipment(next, operation); err != nil {
		return err
	}
	s.owned = next
	s.persisted = cloneEquipmentSnapshot(next)
	return nil
}

func (s *EquipmentInventory) persistEquipment(next equipmentSnapshot, operation string) error {
	return s.persistEquipmentState(next, nil, false, operation)
}

func (s *EquipmentInventory) persistEquipmentState(next equipmentSnapshot, pending *equipmentOptionRerollPending, pendingDirty bool, operation string) error {
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
	if err := s.store.SaveWithEntries("equipment", data, changes); err != nil {
		return fmt.Errorf("player: persist equipment %s: %w", operation, err)
	}
	s.corePresent = true
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
	if err := s.commitLocked(next, "use"); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist equipment use: %w", err)
	}
	return 35, wire.AppendBytes(nil, 1, CharacterWire(character)), true, nil
}
