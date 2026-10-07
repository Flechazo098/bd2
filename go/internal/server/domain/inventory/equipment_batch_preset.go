package inventory

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
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

func (s *EquipmentInventory) loadEquipmentPresets(ctx command.Context, entries stateio.ScopedEntryStore) error {
	raw, err := entries.ListEntries(ctx.State, "equipment", "presets")
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

func (s *EquipmentInventory) persistEquipmentPreset(ctx command.Context, preset equipmentPreset, operation string) error {
	payload, err := json.Marshal(preset)
	if err != nil {
		return err
	}
	key := equipmentPresetKey{CharacterIndex: preset.CharacterIndex, Slot: preset.Slot}
	change := stateio.EntryMutation{Bucket: "presets", Key: equipmentPresetStorageKey(key), Payload: payload}
	if err := s.store.SaveWithEntries(ctx.State, "equipment", nil, []stateio.EntryMutation{change}); err != nil {
		return fmt.Errorf("player: persist equipment preset %s: %w", operation, err)
	}
	return nil
}

func cloneEquipmentPreset(preset equipmentPreset) equipmentPreset {
	preset.Items = append([]equipmentPresetItem(nil), preset.Items...)
	return preset
}
