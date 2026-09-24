package player

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

const equipmentSlotCount = 5

type equipmentBatchUse struct {
	CharacterIndex uint64
	Equipment      []uint64
}

type equipmentPresetKey struct {
	CharacterIndex uint64
	Slot           uint64
}

type equipmentPresetItem struct {
	Type           uint64 `json:"type"`
	EquipmentIndex uint64 `json:"equipment_index"`
}

type equipmentPreset struct {
	CharacterIndex uint64                `json:"character_index"`
	Slot           uint64                `json:"slot"`
	Name           string                `json:"name"`
	ResourceID     uint64                `json:"resource_id"`
	ResourceColor  uint64                `json:"resource_color"`
	Items          []equipmentPresetItem `json:"items"`
}

func (s *EquipmentInventory) batchUse(request []byte) (int, []byte, bool, error) {
	entries, err := decodeEquipmentBatchUse(request)
	if err != nil {
		return 0, nil, true, err
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipBatchUse character store unavailable")
	}
	requestedCharacters := make(map[uint64]bool, len(entries))
	for _, entry := range entries {
		if requestedCharacters[entry.CharacterIndex] {
			return 0, nil, true, fmt.Errorf("player: EquipBatchUse repeats character %d", entry.CharacterIndex)
		}
		requestedCharacters[entry.CharacterIndex] = true
		if _, found := s.characters.Find(entry.CharacterIndex); !found {
			return 0, nil, true, fmt.Errorf("player: EquipBatchUse unknown character %d", entry.CharacterIndex)
		}
	}

	s.mu.Lock()
	if len(s.slots) == 0 {
		s.mu.Unlock()
		return 0, nil, true, errors.New("player: EquipBatchUse slot design unavailable")
	}
	desired := make(map[uint64]uint64)
	for _, entry := range entries {
		for slot, index := range entry.Equipment {
			if index == 0 {
				continue
			}
			if desired[index] != 0 {
				s.mu.Unlock()
				return 0, nil, true, fmt.Errorf("player: EquipBatchUse repeats equipment %d", index)
			}
			position := s.equipmentPositionLocked(index)
			if position < 0 {
				s.mu.Unlock()
				return 0, nil, true, fmt.Errorf("player: EquipBatchUse unknown equipment %d", index)
			}
			item := s.owned.Equipment[position]
			if designedSlot, ok := s.slots[item.ID]; !ok || designedSlot != uint64(slot) {
				s.mu.Unlock()
				return 0, nil, true, fmt.Errorf("player: EquipBatchUse equipment %d does not belong in slot %d", index, slot)
			}
			if item.UseChar != 0 && item.UseChar != entry.CharacterIndex && !requestedCharacters[item.UseChar] {
				s.mu.Unlock()
				return 0, nil, true, fmt.Errorf("player: EquipBatchUse equipment %d is still used by character %d", index, item.UseChar)
			}
			desired[index] = entry.CharacterIndex
		}
	}

	next := cloneEquipmentSnapshot(s.owned)
	affected := make(map[uint64]bool, len(entries))
	for character := range requestedCharacters {
		affected[character] = true
	}
	for i := range next.Equipment {
		item := &next.Equipment[i]
		if requestedCharacters[item.UseChar] {
			item.UseChar = 0
		}
	}
	for i := range next.Equipment {
		item := &next.Equipment[i]
		character := desired[item.InvenIndex]
		if character == 0 {
			continue
		}
		if item.UseChar != 0 && item.UseChar != character {
			affected[item.UseChar] = true
		}
		item.UseChar = character
	}
	if err := s.commitLocked(next, "batch use"); err != nil {
		s.mu.Unlock()
		return 0, nil, true, err
	}
	s.mu.Unlock()

	characterIndices := make([]uint64, 0, len(affected))
	for index := range affected {
		characterIndices = append(characterIndices, index)
	}
	sort.Slice(characterIndices, func(i, j int) bool { return characterIndices[i] < characterIndices[j] })
	var response []byte
	for _, index := range characterIndices {
		if character, found := s.characters.Find(index); found {
			response = wire.AppendBytes(response, 1, CharacterWire(character))
		}
	}
	return 276, response, true, nil
}

func decodeEquipmentBatchUse(request []byte) ([]equipmentBatchUse, error) {
	var result []equipmentBatchUse
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: EquipBatchUse invalid batch entry")
		}
		entry := equipmentBatchUse{}
		var characterSeen bool
		if err := wire.Walk(field.Value, func(nested wire.Field) error {
			switch nested.Number {
			case 1:
				if characterSeen || nested.Type != 0 {
					return errors.New("player: EquipBatchUse invalid character")
				}
				entry.CharacterIndex, _ = binary.Uvarint(nested.Value)
				characterSeen = true
			case 2:
				values, err := decodeRepeatedUint64(nested)
				if err != nil {
					return errors.New("player: EquipBatchUse invalid equipment list")
				}
				entry.Equipment = append(entry.Equipment, values...)
			}
			return nil
		}); err != nil {
			return err
		}
		if !characterSeen || entry.CharacterIndex == 0 || len(entry.Equipment) != equipmentSlotCount {
			return errors.New("player: EquipBatchUse requires one character and five equipment slots")
		}
		result = append(result, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("player: EquipBatchUse has no batch entries")
	}
	return result, nil
}

func decodeRepeatedUint64(field wire.Field) ([]uint64, error) {
	switch field.Type {
	case 0:
		value, count := binary.Uvarint(field.Value)
		if count <= 0 {
			return nil, wire.ErrMalformed
		}
		return []uint64{value}, nil
	case 2:
		var result []uint64
		for offset := 0; offset < len(field.Value); {
			value, count := binary.Uvarint(field.Value[offset:])
			if count <= 0 {
				return nil, wire.ErrMalformed
			}
			result = append(result, value)
			offset += count
		}
		return result, nil
	default:
		return nil, wire.ErrMalformed
	}
}

func (s *EquipmentInventory) loadEquipmentPresets(entries stateio.EntryStore) error {
	raw, err := entries.ListEntries("equipment", "presets")
	if err != nil {
		return err
	}
	for key, payload := range raw {
		parsed, err := parseEquipmentPresetKey(key)
		if err != nil {
			return err
		}
		if err := stateio.RequireExactJSONObject(payload, "character_index", "slot", "name", "resource_id", "resource_color", "items"); err != nil {
			return fmt.Errorf("player: incompatible equipment preset %q: %w", key, err)
		}
		var preset equipmentPreset
		if err := json.Unmarshal(payload, &preset); err != nil || preset.CharacterIndex != parsed.CharacterIndex || preset.Slot != parsed.Slot {
			return fmt.Errorf("player: invalid equipment preset %q", key)
		}
		if err := validateEquipmentPresetShape(preset); err != nil {
			return fmt.Errorf("player: invalid equipment preset %q: %w", key, err)
		}
		s.presets[parsed] = preset
	}
	return nil
}

func parseEquipmentPresetKey(value string) (equipmentPresetKey, error) {
	left, right, found := strings.Cut(value, ":")
	if !found {
		return equipmentPresetKey{}, fmt.Errorf("player: invalid equipment preset key %q", value)
	}
	character, characterErr := strconv.ParseUint(left, 10, 64)
	slot, slotErr := strconv.ParseUint(right, 10, 64)
	if characterErr != nil || slotErr != nil || character == 0 || slot < 1 || slot > 5 || value != equipmentPresetStorageKey(equipmentPresetKey{CharacterIndex: character, Slot: slot}) {
		return equipmentPresetKey{}, fmt.Errorf("player: invalid equipment preset key %q", value)
	}
	return equipmentPresetKey{CharacterIndex: character, Slot: slot}, nil
}

func equipmentPresetStorageKey(key equipmentPresetKey) string {
	return strconv.FormatUint(key.CharacterIndex, 10) + ":" + strconv.FormatUint(key.Slot, 10)
}

func validateEquipmentPresetShape(preset equipmentPreset) error {
	if preset.CharacterIndex == 0 || preset.Slot < 1 || preset.Slot > 5 || !utf8.ValidString(preset.Name) ||
		utf8.RuneCountInString(preset.Name) < 1 || utf8.RuneCountInString(preset.Name) > 16 ||
		preset.ResourceID < 1 || preset.ResourceID > 21 || preset.ResourceColor > 5 || len(preset.Items) != equipmentSlotCount {
		return errors.New("invalid fields")
	}
	seen := make(map[uint64]bool, equipmentSlotCount)
	for _, item := range preset.Items {
		if item.Type >= equipmentSlotCount || seen[item.Type] {
			return errors.New("invalid equipment slots")
		}
		seen[item.Type] = true
	}
	return nil
}

func (s *EquipmentInventory) presetInfo() (int, []byte, bool, error) {
	s.mu.Lock()
	presets := make([]equipmentPreset, 0, len(s.presets))
	for _, preset := range s.presets {
		presets = append(presets, cloneEquipmentPreset(preset))
	}
	s.mu.Unlock()
	sort.Slice(presets, func(i, j int) bool {
		if presets[i].CharacterIndex != presets[j].CharacterIndex {
			return presets[i].CharacterIndex < presets[j].CharacterIndex
		}
		return presets[i].Slot < presets[j].Slot
	})
	var response []byte
	for i := 0; i < len(presets); {
		character := presets[i].CharacterIndex
		var encoded []byte
		encoded = wire.AppendVarint(encoded, 1, character)
		for i < len(presets) && presets[i].CharacterIndex == character {
			encoded = wire.AppendBytes(encoded, 2, equipmentPresetWire(presets[i]))
			i++
		}
		response = wire.AppendBytes(response, 1, encoded)
	}
	return 253, response, true, nil
}

func equipmentPresetWire(preset equipmentPreset) []byte {
	var out []byte
	if preset.Name != "" {
		out = wire.AppendString(out, 1, preset.Name)
	}
	out = wire.AppendVarint(out, 2, preset.Slot)
	if preset.ResourceID != 0 {
		out = wire.AppendVarint(out, 3, preset.ResourceID)
	}
	if preset.ResourceColor != 0 {
		out = wire.AppendVarint(out, 4, preset.ResourceColor)
	}
	for _, item := range preset.Items {
		var encoded []byte
		if item.Type != 0 {
			encoded = wire.AppendVarint(encoded, 1, item.Type)
		}
		if item.EquipmentIndex != 0 {
			encoded = wire.AppendVarint(encoded, 2, item.EquipmentIndex)
		}
		out = wire.AppendBytes(out, 5, encoded)
	}
	return out
}

func (s *EquipmentInventory) presetSave(request []byte) (int, []byte, bool, error) {
	preset, err := decodeEquipmentPresetRequest(request, true)
	if err != nil {
		return 0, nil, true, err
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipPresetSave character store unavailable")
	}
	if _, found := s.characters.Find(preset.CharacterIndex); !found {
		return 0, nil, true, fmt.Errorf("player: EquipPresetSave unknown character %d", preset.CharacterIndex)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.slots) == 0 {
		return 0, nil, true, errors.New("player: EquipPresetSave slot design unavailable")
	}
	for _, item := range preset.Items {
		if item.EquipmentIndex == 0 {
			continue
		}
		position := s.equipmentPositionLocked(item.EquipmentIndex)
		if position < 0 {
			return 0, nil, true, fmt.Errorf("player: EquipPresetSave unknown equipment %d", item.EquipmentIndex)
		}
		equipment := s.owned.Equipment[position]
		if s.slots[equipment.ID] != item.Type {
			return 0, nil, true, fmt.Errorf("player: EquipPresetSave equipment %d does not match character slot", item.EquipmentIndex)
		}
	}
	if err := s.persistEquipmentPreset(preset, "save"); err != nil {
		return 0, nil, true, err
	}
	s.presets[equipmentPresetKey{CharacterIndex: preset.CharacterIndex, Slot: preset.Slot}] = cloneEquipmentPreset(preset)
	return 254, nil, true, nil
}

func (s *EquipmentInventory) presetNameChange(request []byte) (int, []byte, bool, error) {
	metadata, err := decodeEquipmentPresetRequest(request, false)
	if err != nil {
		return 0, nil, true, err
	}
	key := equipmentPresetKey{CharacterIndex: metadata.CharacterIndex, Slot: metadata.Slot}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipPresetNameChange character store unavailable")
	}
	if _, found := s.characters.Find(metadata.CharacterIndex); !found {
		return 0, nil, true, fmt.Errorf("player: EquipPresetNameChange unknown character %d", metadata.CharacterIndex)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preset, found := s.presets[key]
	if !found {
		preset = metadata
	}
	preset.Name = metadata.Name
	preset.ResourceID = metadata.ResourceID
	preset.ResourceColor = metadata.ResourceColor
	if err := s.persistEquipmentPreset(preset, "name change"); err != nil {
		return 0, nil, true, err
	}
	s.presets[key] = cloneEquipmentPreset(preset)
	return 259, nil, true, nil
}

func decodeEquipmentPresetRequest(request []byte, withItems bool) (equipmentPreset, error) {
	character, found, err := wire.Varint(request, 2)
	if err != nil || !found || character == 0 {
		return equipmentPreset{}, errors.New("player: equipment preset request missing character")
	}
	slot, found, err := wire.Varint(request, 3)
	if err != nil || !found || slot < 1 || slot > 5 {
		return equipmentPreset{}, errors.New("player: equipment preset request invalid slot")
	}
	nameBytes, _, err := wire.Bytes(request, 4)
	if err != nil || !utf8.Valid(nameBytes) {
		return equipmentPreset{}, errors.New("player: equipment preset request invalid name")
	}
	resourceID, _, err := wire.Varint(request, 5)
	if err != nil || resourceID < 1 || resourceID > 21 {
		return equipmentPreset{}, errors.New("player: equipment preset request invalid resource")
	}
	color, _, err := wire.Varint(request, 6)
	if err != nil || color > 5 {
		return equipmentPreset{}, errors.New("player: equipment preset request invalid color")
	}
	preset := equipmentPreset{CharacterIndex: character, Slot: slot, Name: string(nameBytes), ResourceID: resourceID, ResourceColor: color}
	if withItems {
		err = wire.Walk(request, func(field wire.Field) error {
			if field.Number != 7 {
				return nil
			}
			if field.Type != 2 {
				return errors.New("player: EquipPresetSave invalid equipment entry")
			}
			typeID, _, err := wire.Varint(field.Value, 1)
			if err != nil {
				return err
			}
			index, _, err := wire.Varint(field.Value, 2)
			if err != nil {
				return err
			}
			preset.Items = append(preset.Items, equipmentPresetItem{Type: typeID, EquipmentIndex: index})
			return nil
		})
		if err != nil {
			return equipmentPreset{}, err
		}
	} else {
		preset.Items = make([]equipmentPresetItem, equipmentSlotCount)
		for i := range preset.Items {
			preset.Items[i].Type = uint64(i)
		}
	}
	if err := validateEquipmentPresetShape(preset); err != nil {
		return equipmentPreset{}, fmt.Errorf("player: equipment preset request: %w", err)
	}
	sort.Slice(preset.Items, func(i, j int) bool { return preset.Items[i].Type < preset.Items[j].Type })
	return preset, nil
}

func (s *EquipmentInventory) persistEquipmentPreset(preset equipmentPreset, operation string) error {
	payload, err := json.Marshal(preset)
	if err != nil {
		return err
	}
	key := equipmentPresetKey{CharacterIndex: preset.CharacterIndex, Slot: preset.Slot}
	change := stateio.EntryMutation{Bucket: "presets", Key: equipmentPresetStorageKey(key), Payload: payload}
	if err := s.store.SaveWithEntries("equipment", nil, []stateio.EntryMutation{change}); err != nil {
		return fmt.Errorf("player: persist equipment preset %s: %w", operation, err)
	}
	return nil
}

func cloneEquipmentPreset(preset equipmentPreset) equipmentPreset {
	preset.Items = append([]equipmentPresetItem(nil), preset.Items...)
	return preset
}
