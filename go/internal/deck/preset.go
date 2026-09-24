package deck

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"bd2server/internal/player"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

const (
	presetBaseCount = 5
	presetMaxCount  = 12
	presetSlotPrice = 2000
)

type Preset struct {
	Name          string        `json:"name"`
	ResourceID    uint64        `json:"resource_id"`
	ResourceColor uint64        `json:"resource_color"`
	Slot          uint64        `json:"slot"`
	Decks         []PresetDeck  `json:"decks"`
	Blesses       []PresetBless `json:"blesses"`
}

type PresetDeck struct {
	Deck         DeckEntry             `json:"deck"`
	CostumeIndex uint64                `json:"costume_index"`
	Equipment    []PresetEquipmentItem `json:"equipment"`
	Team         uint64                `json:"team"`
}

type PresetEquipmentItem struct {
	Type  uint64 `json:"type"`
	Index uint64 `json:"index"`
}

type PresetBless struct {
	DeckType uint64   `json:"deck_type"`
	IDs      []uint64 `json:"ids"`
}

type CostumeSetting struct {
	CharacterIndex uint64               `json:"character_index"`
	Sequence       []CostumeSettingItem `json:"sequence"`
	BattleMode     uint64               `json:"battle_mode"`
	MonsterID      uint64               `json:"monster_id"`
}

type CostumeSettingItem struct {
	CostumeIndex int64  `json:"costume_index"`
	BurstLevel   uint64 `json:"burst_level"`
}

func (s *Store) AttachPresetRuntime(wallet *player.Wallet, characters *player.CharacterStore, equipment *player.EquipmentInventory, collection *player.CollectionStore) error {
	if wallet == nil || characters == nil || equipment == nil || collection == nil {
		return errors.New("deck: incomplete preset runtime")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wallet, s.characters, s.equipment, s.collection = wallet, characters, equipment, collection
	return s.validatePresetOwnershipLocked()
}

func (s *Store) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = id
	s.replies = map[string]deckReply{}
}

func (s *Store) PresetSlotCount() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.presetSlots
}

func (s *Store) loadPresetEntries() error {
	rawConfig, found, err := s.storage.LoadEntry("deck", "preset_config", "slots")
	if err != nil {
		return err
	}
	if found {
		if err := json.Unmarshal(rawConfig, &s.presetSlots); err != nil || s.presetSlots < presetBaseCount || s.presetSlots > presetMaxCount {
			return errors.New("deck: invalid preset slot configuration")
		}
	}
	raw, err := s.storage.ListEntries("deck", "presets")
	if err != nil {
		return err
	}
	for key, payload := range raw {
		slot, err := strconv.ParseUint(key, 10, 64)
		if err != nil || key != strconv.FormatUint(slot, 10) {
			return fmt.Errorf("deck: invalid preset key %q", key)
		}
		var preset Preset
		if err := json.Unmarshal(payload, &preset); err != nil || preset.Slot != slot {
			return fmt.Errorf("deck: invalid preset %q", key)
		}
		if err := validatePresetShape(preset, s.presetSlots); err != nil {
			return fmt.Errorf("deck: invalid preset %q: %w", key, err)
		}
		s.presets[slot] = preset
	}
	rawSettings, err := s.storage.ListEntries("deck", "costume_settings")
	if err != nil {
		return err
	}
	for key, payload := range rawSettings {
		index, err := strconv.ParseUint(key, 10, 64)
		if err != nil || index == 0 || key != strconv.FormatUint(index, 10) {
			return fmt.Errorf("deck: invalid costume setting key %q", key)
		}
		var setting CostumeSetting
		if err := json.Unmarshal(payload, &setting); err != nil || setting.CharacterIndex != index {
			return fmt.Errorf("deck: invalid costume setting %q", key)
		}
		s.costumeSettings[index] = setting
	}
	return nil
}

func (s *Store) validatePresetOwnershipLocked() error {
	if s.characters == nil || s.collection == nil || s.equipment == nil {
		return nil
	}
	for _, preset := range s.presets {
		if err := s.validatePresetOwnedLocked(preset); err != nil {
			return fmt.Errorf("deck: saved preset %d: %w", preset.Slot, err)
		}
	}
	for _, setting := range s.costumeSettings {
		if _, found := s.characters.Find(setting.CharacterIndex); !found {
			return fmt.Errorf("deck: costume setting references unknown character %d", setting.CharacterIndex)
		}
		for _, item := range setting.Sequence {
			if item.CostumeIndex > 0 {
				if _, found := s.collection.CostumeByIndex(uint64(item.CostumeIndex)); !found {
					return fmt.Errorf("deck: costume setting references unknown costume %d", item.CostumeIndex)
				}
			}
		}
	}
	return nil
}

func validatePresetShape(p Preset, slotCount uint64) error {
	if p.Slot >= slotCount || p.ResourceID > 21 || p.ResourceColor > 5 || !validPresetName(p.Name) || len(p.Decks) > 5 {
		return errors.New("invalid metadata or deck count")
	}
	characters, positions, sequences := map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}
	for _, deck := range p.Decks {
		if deck.Deck.CharacterInvenIndex == 0 || deck.Deck.CostumeInvenIndex > 11 || deck.Deck.Slot == 0 || deck.Deck.Slot > 5 || deck.Team != 0 ||
			characters[deck.Deck.CharacterInvenIndex] || positions[deck.Deck.CostumeInvenIndex] || sequences[deck.Deck.Slot] {
			return errors.New("invalid deck entry")
		}
		characters[deck.Deck.CharacterInvenIndex] = true
		positions[deck.Deck.CostumeInvenIndex] = true
		sequences[deck.Deck.Slot] = true
		if len(deck.Equipment) != 5 {
			return errors.New("preset deck requires five equipment slots")
		}
		seen := map[uint64]bool{}
		for _, item := range deck.Equipment {
			if item.Type >= 5 || seen[item.Type] {
				return errors.New("invalid preset equipment slot")
			}
			seen[item.Type] = true
		}
	}
	return nil
}

func validPresetName(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 16 || strings.Contains(value, "<") || strings.Contains(value, ">") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Store) validatePresetOwnedLocked(p Preset) error {
	if err := validatePresetShape(p, s.presetSlots); err != nil {
		return err
	}
	ownedEquipment := make(map[uint64]player.Equipment)
	for _, item := range s.equipment.All() {
		ownedEquipment[item.InvenIndex] = item
	}
	seenEquipment := map[uint64]bool{}
	for _, deck := range p.Decks {
		if _, found := s.characters.Find(deck.Deck.CharacterInvenIndex); !found {
			return fmt.Errorf("unknown character %d", deck.Deck.CharacterInvenIndex)
		}
		if deck.CostumeIndex != 0 {
			costume, found := s.collection.CostumeByIndex(deck.CostumeIndex)
			if !found || costume.UseChar != deck.Deck.CharacterInvenIndex {
				return fmt.Errorf("unknown costume %d", deck.CostumeIndex)
			}
		}
		for _, reference := range deck.Equipment {
			if reference.Index == 0 {
				continue
			}
			if seenEquipment[reference.Index] || ownedEquipment[reference.Index].InvenIndex == 0 {
				return fmt.Errorf("invalid or repeated equipment %d", reference.Index)
			}
			seenEquipment[reference.Index] = true
		}
		binding := player.PresetEquipmentBinding{CharacterIndex: deck.Deck.CharacterInvenIndex, Equipment: make([]uint64, 5)}
		for _, reference := range deck.Equipment {
			binding.Equipment[reference.Type] = reference.Index
		}
		if err := s.equipment.ValidatePresetEquipment([]player.PresetEquipmentBinding{binding}); err != nil {
			return err
		}
	}
	return nil
}

func decodePreset(data []byte) (Preset, error) {
	var p Preset
	name, _, err := wire.Bytes(data, 1)
	if err != nil {
		return p, err
	}
	p.Name = string(name)
	p.ResourceID, _, err = wire.Varint(data, 2)
	if err != nil {
		return p, err
	}
	p.ResourceColor, _, err = wire.Varint(data, 3)
	if err != nil {
		return p, err
	}
	p.Slot, _, err = wire.Varint(data, 4)
	if err != nil {
		return p, err
	}
	err = wire.Walk(data, func(field wire.Field) error {
		if field.Type != 2 {
			return nil
		}
		switch field.Number {
		case 5:
			deck, err := decodePresetDeck(field.Value)
			if err != nil {
				return err
			}
			p.Decks = append(p.Decks, deck)
		case 6:
			bless, err := decodePresetBless(field.Value)
			if err != nil {
				return err
			}
			p.Blesses = append(p.Blesses, bless)
		}
		return nil
	})
	return p, err
}

func decodePresetDeck(data []byte) (PresetDeck, error) {
	var result PresetDeck
	base, found, err := wire.Bytes(data, 1)
	if err != nil || !found {
		return result, errors.New("deck: preset missing deck base")
	}
	character, ok, err := wire.Varint(base, 1)
	if err != nil || !ok || character == 0 {
		return result, errors.New("deck: invalid preset character")
	}
	position, _, err := wire.Varint(base, 2)
	if err != nil {
		return result, err
	}
	sequence, ok, err := wire.Varint(base, 3)
	if err != nil || !ok || sequence == 0 {
		return result, errors.New("deck: invalid preset sequence")
	}
	result.Deck = DeckEntry{CharacterInvenIndex: character, CostumeInvenIndex: position, Slot: sequence}
	result.CostumeIndex, _, err = wire.Varint(data, 2)
	if err != nil {
		return result, err
	}
	result.Team, _, err = wire.Varint(data, 4)
	if err != nil {
		return result, err
	}
	err = wire.Walk(data, func(field wire.Field) error {
		if field.Number != 3 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("deck: invalid preset equipment")
		}
		typeID, _, err := wire.Varint(field.Value, 1)
		if err != nil {
			return err
		}
		index, _, err := wire.Varint(field.Value, 2)
		if err != nil {
			return err
		}
		result.Equipment = append(result.Equipment, PresetEquipmentItem{Type: typeID, Index: index})
		return nil
	})
	sort.Slice(result.Equipment, func(i, j int) bool { return result.Equipment[i].Type < result.Equipment[j].Type })
	return result, err
}

func decodePresetBless(data []byte) (PresetBless, error) {
	var result PresetBless
	result.DeckType, _, _ = wire.Varint(data, 1)
	err := wire.Walk(data, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		values, err := repeatedUint64(field)
		if err != nil {
			return err
		}
		result.IDs = append(result.IDs, values...)
		return nil
	})
	return result, err
}

func repeatedUint64(field wire.Field) ([]uint64, error) {
	if field.Type == 0 {
		value, count := binary.Uvarint(field.Value)
		if count <= 0 {
			return nil, wire.ErrMalformed
		}
		return []uint64{value}, nil
	}
	if field.Type != 2 {
		return nil, wire.ErrMalformed
	}
	var out []uint64
	for offset := 0; offset < len(field.Value); {
		value, count := binary.Uvarint(field.Value[offset:])
		if count <= 0 {
			return nil, wire.ErrMalformed
		}
		out = append(out, value)
		offset += count
	}
	return out, nil
}

func presetWire(p Preset) []byte {
	var out []byte
	if p.Name != "" {
		out = wire.AppendString(out, 1, p.Name)
	}
	if p.ResourceID != 0 {
		out = wire.AppendVarint(out, 2, p.ResourceID)
	}
	if p.ResourceColor != 0 {
		out = wire.AppendVarint(out, 3, p.ResourceColor)
	}
	if p.Slot != 0 {
		out = wire.AppendVarint(out, 4, p.Slot)
	}
	for _, deck := range p.Decks {
		out = wire.AppendBytes(out, 5, presetDeckWire(deck))
	}
	for _, bless := range p.Blesses {
		var b []byte
		if bless.DeckType != 0 {
			b = wire.AppendVarint(b, 1, bless.DeckType)
		}
		for _, id := range bless.IDs {
			b = wire.AppendVarint(b, 2, id)
		}
		out = wire.AppendBytes(out, 6, b)
	}
	return out
}

func presetDeckWire(deck PresetDeck) []byte {
	base := wire.AppendVarint(nil, 1, deck.Deck.CharacterInvenIndex)
	if deck.Deck.CostumeInvenIndex != 0 {
		base = wire.AppendVarint(base, 2, deck.Deck.CostumeInvenIndex)
	}
	base = wire.AppendVarint(base, 3, deck.Deck.Slot)
	out := wire.AppendBytes(nil, 1, base)
	if deck.CostumeIndex != 0 {
		out = wire.AppendVarint(out, 2, deck.CostumeIndex)
	}
	for _, item := range deck.Equipment {
		var b []byte
		if item.Type != 0 {
			b = wire.AppendVarint(b, 1, item.Type)
		}
		if item.Index != 0 {
			b = wire.AppendVarint(b, 2, item.Index)
		}
		out = wire.AppendBytes(out, 3, b)
	}
	if deck.Team != 0 {
		out = wire.AppendVarint(out, 4, deck.Team)
	}
	return out
}

func (s *Store) presetCacheKey(kind string, seq uint64) string {
	return kind + ":" + s.sessionID + ":" + strconv.FormatUint(seq, 10)
}

func (s *Store) persistPresetLocked(p Preset) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	core, err := s.corePayloadLocked()
	if err != nil {
		return err
	}
	return s.storage.SaveWithEntries("deck", core, []stateio.EntryMutation{{Bucket: "presets", Key: strconv.FormatUint(p.Slot, 10), Payload: payload}})
}

func (s *Store) corePayloadLocked() ([]byte, error) {
	payload, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

func (s *Store) cachedReplyLocked(kind string, seq uint64) (deckReply, bool) {
	reply, found := s.replies[s.presetCacheKey(kind, seq)]
	if found {
		reply.body = append([]byte(nil), reply.body...)
	}
	return reply, found
}

func (s *Store) rememberReplyLocked(kind string, seq uint64, code int, body []byte) {
	s.replies[s.presetCacheKey(kind, seq)] = deckReply{code: code, body: append([]byte(nil), body...)}
}

func requestSequence(request []byte) (uint64, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, errors.New("deck: invalid request sequence")
	}
	return seq, nil
}

func (s *Store) handlePresetInfo(request []byte) (int, []byte, bool, error) {
	if _, err := requestSequence(request); err != nil {
		return 0, nil, true, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	slots := make([]uint64, 0, len(s.presets))
	for slot := range s.presets {
		if slot < s.presetSlots {
			slots = append(slots, slot)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	var response []byte
	for _, slot := range slots {
		response = wire.AppendBytes(response, 1, presetWire(s.presets[slot]))
	}
	return 178, response, true, nil
}

func (s *Store) handlePresetSave(request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	raw, found, err := wire.Bytes(request, 2)
	if err != nil || !found {
		return 0, nil, true, errors.New("deck: PresetSave missing preset")
	}
	preset, err := decodePreset(raw)
	if err != nil {
		return 0, nil, true, fmt.Errorf("deck: decode preset: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reply, found := s.cachedReplyLocked("save", seq); found {
		return reply.code, reply.body, true, nil
	}
	if s.characters == nil || s.collection == nil || s.equipment == nil {
		return 0, nil, true, errors.New("deck: preset runtime unavailable")
	}
	if err := s.validatePresetOwnedLocked(preset); err != nil {
		return 0, nil, true, fmt.Errorf("deck: invalid preset: %w", err)
	}
	if err := s.persistPresetLocked(preset); err != nil {
		return 0, nil, true, fmt.Errorf("deck: persist preset: %w", err)
	}
	s.presets[preset.Slot] = preset
	s.rememberReplyLocked("save", seq, 179, nil)
	return 179, nil, true, nil
}

func (s *Store) handlePresetAddSlot(request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	count, found, err := wire.Varint(request, 2)
	if err != nil || !found || count == 0 {
		return 0, nil, true, errors.New("deck: PresetAddSlot invalid count")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reply, found := s.cachedReplyLocked("add-slot", seq); found {
		return reply.code, reply.body, true, nil
	}
	if s.wallet == nil {
		return 0, nil, true, errors.New("deck: preset wallet unavailable")
	}
	if s.presetSlots > presetMaxCount || count > presetMaxCount-s.presetSlots {
		return 0, nil, true, errors.New("deck: preset slot limit exceeded")
	}
	if count > ^uint64(0)/presetSlotPrice {
		return 0, nil, true, errors.New("deck: preset slot price overflow")
	}
	identity := "preset-slot:" + s.sessionID + ":" + strconv.FormatUint(seq, 10)
	if _, err := s.wallet.SpendGoldOnce(identity, count*presetSlotPrice); err != nil {
		return 0, nil, true, fmt.Errorf("deck: buy preset slot: %w", err)
	}
	next := s.presetSlots + count
	payload, err := json.Marshal(next)
	if err != nil {
		return 0, nil, true, err
	}
	core, err := s.corePayloadLocked()
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.storage.SaveWithEntries("deck", core, []stateio.EntryMutation{{Bucket: "preset_config", Key: "slots", Payload: payload}}); err != nil {
		return 0, nil, true, fmt.Errorf("deck: persist preset slots: %w", err)
	}
	s.presetSlots = next
	s.rememberReplyLocked("add-slot", seq, 180, nil)
	return 180, nil, true, nil
}

func (s *Store) handlePresetInfoChange(request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	nameBytes, _, err := wire.Bytes(request, 2)
	if err != nil {
		return 0, nil, true, errors.New("deck: invalid preset name")
	}
	resourceID, _, err := wire.Varint(request, 3)
	if err != nil {
		return 0, nil, true, errors.New("deck: invalid preset icon")
	}
	color, _, err := wire.Varint(request, 4)
	if err != nil {
		return 0, nil, true, errors.New("deck: invalid preset color")
	}
	// Ordinary preset slots are zero-based. Proto3 omits slot=0, so an absent
	// field 5 is the first slot rather than a malformed request.
	slot, _, err := wire.Varint(request, 5)
	if err != nil {
		return 0, nil, true, errors.New("deck: invalid preset slot")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reply, found := s.cachedReplyLocked("info-change", seq); found {
		return reply.code, reply.body, true, nil
	}
	preset, exists := s.presets[slot]
	if !exists {
		preset = Preset{Slot: slot, Decks: []PresetDeck{}, Blesses: []PresetBless{}}
	}
	preset.Name, preset.ResourceID, preset.ResourceColor = string(nameBytes), resourceID, color
	if err := validatePresetShape(preset, s.presetSlots); err != nil {
		return 0, nil, true, fmt.Errorf("deck: invalid preset metadata: %w", err)
	}
	if err := s.persistPresetLocked(preset); err != nil {
		return 0, nil, true, err
	}
	s.presets[slot] = preset
	s.rememberReplyLocked("info-change", seq, 278, nil)
	return 278, nil, true, nil
}

func (s *Store) handlePresetDelete(request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	var slots []uint64
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		values, err := repeatedUint64(field)
		if err != nil {
			return err
		}
		slots = append(slots, values...)
		return nil
	}); err != nil || len(slots) == 0 {
		return 0, nil, true, errors.New("deck: invalid preset delete slots")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reply, found := s.cachedReplyLocked("delete", seq); found {
		return reply.code, reply.body, true, nil
	}
	seen := make(map[uint64]bool, len(slots))
	changes := make([]stateio.EntryMutation, 0, len(slots))
	for _, slot := range slots {
		if slot >= s.presetSlots || seen[slot] {
			return 0, nil, true, errors.New("deck: invalid or duplicate preset delete slot")
		}
		seen[slot] = true
		changes = append(changes, stateio.EntryMutation{Bucket: "presets", Key: strconv.FormatUint(slot, 10), Delete: true})
	}
	core, err := s.corePayloadLocked()
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.storage.SaveWithEntries("deck", core, changes); err != nil {
		return 0, nil, true, err
	}
	for slot := range seen {
		delete(s.presets, slot)
	}
	// 2.35.10 contains the request/response classes but no PacketCode enum
	// member. Code zero is the same compatibility fallback used for other
	// unnumbered local endpoints; it must not be treated as an official value.
	s.rememberReplyLocked("delete", seq, 0, nil)
	return 0, nil, true, nil
}

func (s *Store) handlePresetUse(request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	// PresetUse is also zero-based and slot=0 is omitted by proto3.
	slot, _, err := wire.Varint(request, 2)
	if err != nil {
		return 0, nil, true, errors.New("deck: PresetUse missing slot")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reply, found := s.cachedReplyLocked("use", seq); found {
		return reply.code, reply.body, true, nil
	}
	preset, found := s.presets[slot]
	if !found || len(preset.Decks) == 0 {
		return 0, nil, true, errors.New("deck: PresetUse references an empty slot")
	}
	if s.characters == nil || s.collection == nil || s.equipment == nil {
		return 0, nil, true, errors.New("deck: preset runtime unavailable")
	}
	if err := s.validatePresetOwnedLocked(preset); err != nil {
		return 0, nil, true, fmt.Errorf("deck: stale preset: %w", err)
	}
	deckEntries := make([]DeckEntry, 0, len(preset.Decks))
	assignments := make(map[uint64]uint64, len(preset.Decks))
	bindings := make([]player.PresetEquipmentBinding, 0, len(preset.Decks))
	for _, entry := range preset.Decks {
		deckEntries = append(deckEntries, entry.Deck)
		assignments[entry.Deck.CharacterInvenIndex] = entry.CostumeIndex
		equipment := make([]uint64, 5)
		for _, item := range entry.Equipment {
			equipment[item.Type] = item.Index
		}
		bindings = append(bindings, player.PresetEquipmentBinding{CharacterIndex: entry.Deck.CharacterInvenIndex, Equipment: equipment})
	}
	if _, err := s.characters.ApplyPresetCostumes(assignments); err != nil {
		return 0, nil, true, fmt.Errorf("deck: apply preset costumes: %w", err)
	}
	affected, err := s.equipment.ApplyPresetEquipment(bindings)
	if err != nil {
		return 0, nil, true, fmt.Errorf("deck: apply preset equipment: %w", err)
	}
	next := clone(s.state)
	next.Deck = append([]DeckEntry(nil), deckEntries...)
	for character, costume := range assignments {
		next.Costumes[character] = costume
	}
	if err := s.commit(next); err != nil {
		return 0, nil, true, fmt.Errorf("deck: persist applied preset: %w", err)
	}
	var response []byte
	for _, entry := range deckEntries {
		base := wire.AppendVarint(nil, 1, entry.CharacterInvenIndex)
		if entry.CostumeInvenIndex != 0 {
			base = wire.AppendVarint(base, 2, entry.CostumeInvenIndex)
		}
		base = wire.AppendVarint(base, 3, entry.Slot)
		response = wire.AppendBytes(response, 1, base)
	}
	characterSet := make(map[uint64]bool, len(assignments)+len(affected))
	for index := range assignments {
		characterSet[index] = true
	}
	for _, character := range affected {
		characterSet[character.InvenIndex] = true
	}
	indices := make([]uint64, 0, len(characterSet))
	for index := range characterSet {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	for _, index := range indices {
		if character, found := s.characters.Find(index); found {
			response = wire.AppendBytes(response, 2, player.CharacterWire(character))
		}
	}
	for _, binding := range bindings {
		info := wire.AppendVarint(nil, 1, binding.CharacterIndex)
		for _, index := range binding.Equipment {
			info = wire.AppendVarint(info, 2, index)
		}
		response = wire.AppendBytes(response, 3, info)
	}
	s.rememberReplyLocked("use", seq, 409, response)
	return 409, response, true, nil
}

func costumeSettingWire(setting CostumeSetting) []byte {
	out := wire.AppendVarint(nil, 1, setting.CharacterIndex)
	for _, item := range setting.Sequence {
		entry := wire.AppendVarint(nil, 1, uint64(item.CostumeIndex))
		if item.BurstLevel != 0 {
			entry = wire.AppendVarint(entry, 2, item.BurstLevel)
		}
		out = wire.AppendBytes(out, 2, entry)
	}
	if setting.BattleMode != 0 {
		out = wire.AppendVarint(out, 3, setting.BattleMode)
	}
	if setting.MonsterID != 0 {
		out = wire.AppendVarint(out, 4, setting.MonsterID)
	}
	return out
}

func decodeCostumeSetting(data []byte) (CostumeSetting, error) {
	var result CostumeSetting
	character, found, err := wire.Varint(data, 1)
	if err != nil || !found || character == 0 {
		return result, errors.New("deck: costume setting missing character")
	}
	result.CharacterIndex = character
	result.BattleMode, _, err = wire.Varint(data, 3)
	if err != nil {
		return result, err
	}
	result.MonsterID, _, err = wire.Varint(data, 4)
	if err != nil {
		return result, err
	}
	err = wire.Walk(data, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("deck: invalid costume setting sequence")
		}
		raw, found, err := wire.Varint(field.Value, 1)
		if err != nil || !found {
			return errors.New("deck: costume setting entry missing costume")
		}
		burst, _, err := wire.Varint(field.Value, 2)
		if err != nil {
			return err
		}
		result.Sequence = append(result.Sequence, CostumeSettingItem{CostumeIndex: int64(raw), BurstLevel: burst})
		return nil
	})
	return result, err
}

func (s *Store) validateCostumeSettingLocked(setting CostumeSetting) error {
	if s.characters == nil || s.collection == nil {
		return errors.New("deck: costume setting runtime unavailable")
	}
	if setting.BattleMode != 0 || setting.MonsterID != 0 {
		return errors.New("deck: ordinary costume setting requires normal battle mode")
	}
	if _, found := s.characters.Find(setting.CharacterIndex); !found {
		return fmt.Errorf("deck: unknown costume setting character %d", setting.CharacterIndex)
	}
	for _, item := range setting.Sequence {
		if item.CostumeIndex <= 0 {
			if item.CostumeIndex != 0 && item.CostumeIndex != -1 {
				return fmt.Errorf("deck: invalid costume setting sentinel %d", item.CostumeIndex)
			}
			continue
		}
		costume, found := s.collection.CostumeByIndex(uint64(item.CostumeIndex))
		if !found || costume.UseChar != setting.CharacterIndex {
			return fmt.Errorf("deck: costume %d does not belong to character %d", item.CostumeIndex, setting.CharacterIndex)
		}
		if item.BurstLevel > costume.BurstLevel {
			return fmt.Errorf("deck: costume %d burst level exceeds owned level", item.CostumeIndex)
		}
	}
	return nil
}

func (s *Store) handleCostumeSettingInfo(request []byte) (int, []byte, bool, error) {
	if _, err := requestSequence(request); err != nil {
		return 0, nil, true, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	indices := make([]uint64, 0, len(s.costumeSettings))
	for index := range s.costumeSettings {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	var response []byte
	for _, index := range indices {
		response = wire.AppendBytes(response, 1, costumeSettingWire(s.costumeSettings[index]))
	}
	return 397, response, true, nil
}

func (s *Store) handleCostumeSettingSave(request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	raw, found, err := wire.Bytes(request, 2)
	if err != nil || !found {
		return 0, nil, true, errors.New("deck: DeckCostumeSettingSave missing setting")
	}
	setting, err := decodeCostumeSetting(raw)
	if err != nil {
		return 0, nil, true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reply, found := s.cachedReplyLocked("costume-setting-save", seq); found {
		return reply.code, reply.body, true, nil
	}
	if err := s.validateCostumeSettingLocked(setting); err != nil {
		return 0, nil, true, err
	}
	payload, err := json.Marshal(setting)
	if err != nil {
		return 0, nil, true, err
	}
	change := stateio.EntryMutation{Bucket: "costume_settings", Key: strconv.FormatUint(setting.CharacterIndex, 10), Payload: payload}
	core, err := s.corePayloadLocked()
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.storage.SaveWithEntries("deck", core, []stateio.EntryMutation{change}); err != nil {
		return 0, nil, true, err
	}
	s.costumeSettings[setting.CharacterIndex] = setting
	s.rememberReplyLocked("costume-setting-save", seq, 398, nil)
	return 398, nil, true, nil
}
