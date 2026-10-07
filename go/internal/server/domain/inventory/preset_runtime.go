package inventory

import (
	"bd2server/internal/server/domain/command"
	"errors"
	"fmt"
)

// PresetEquipmentBinding is one character's complete five-slot equipment
// target used by the ordinary party preset service.
type PresetEquipmentBinding struct {
	CharacterIndex uint64
	Equipment      []uint64
}

// ValidatePresetEquipment checks the complete fixed-width target without
// mutating ownership. It lets a cross-domain PresetUse fail before any
// character or deck state has been changed.
func (s *EquipmentInventory) ValidatePresetEquipment(ctx command.Context, bindings []PresetEquipmentBinding) error {
	if len(bindings) == 0 {
		return errors.New("player: empty preset equipment bindings")
	}
	seenCharacters := make(map[uint64]bool, len(bindings))
	for _, binding := range bindings {
		if binding.CharacterIndex == 0 || seenCharacters[binding.CharacterIndex] || len(binding.Equipment) != equipmentSlotCount {
			return errors.New("player: invalid preset equipment binding")
		}
		seenCharacters[binding.CharacterIndex] = true
		if s.characters == nil {
			return errors.New("player: preset character store unavailable")
		}
		if _, found := s.characters.EquipmentCharacter(ctx, binding.CharacterIndex); !found {
			return fmt.Errorf("player: preset references unknown character %d", binding.CharacterIndex)
		}
	}
	// CharacterStore.Find may calculate maximum HP, and the production stat
	// calculator reads equipped items. It must run before taking this mutex or
	// preset validation deadlocks by trying to re-enter inventory.EquipmentInventory.

	if len(s.slots) == 0 {
		return errors.New("player: preset equipment slot design unavailable")
	}
	seenEquipment := make(map[uint64]bool)
	for _, binding := range bindings {
		for slot, index := range binding.Equipment {
			if index == 0 {
				continue
			}
			if seenEquipment[index] {
				return fmt.Errorf("player: preset repeats equipment %d", index)
			}
			seenEquipment[index] = true
			position := s.equipmentPositionLocked(index)
			if position < 0 {
				return fmt.Errorf("player: preset references unknown equipment %d", index)
			}
			item := s.owned.Equipment[position]
			if designedSlot, found := s.slots[item.ID]; !found || designedSlot != uint64(slot) {
				return fmt.Errorf("player: preset equipment %d does not belong in slot %d", index, slot)
			}
		}
	}
	return nil
}

// ApplyPresetCostumes changes the currently selected costume for each listed
// character and persists both seeded and collection-owned character records.
