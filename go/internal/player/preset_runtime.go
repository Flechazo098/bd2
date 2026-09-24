package player

import (
	"errors"
	"fmt"
	"sort"

	"bd2server/internal/wire"
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
func (s *EquipmentInventory) ValidatePresetEquipment(bindings []PresetEquipmentBinding) error {
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
		if _, found := s.characters.Find(binding.CharacterIndex); !found {
			return fmt.Errorf("player: preset references unknown character %d", binding.CharacterIndex)
		}
	}
	// CharacterStore.Find may calculate maximum HP, and the production stat
	// calculator reads equipped items. It must run before taking this mutex or
	// preset validation deadlocks by trying to re-enter EquipmentInventory.
	s.mu.Lock()
	defer s.mu.Unlock()
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

// ApplyPresetEquipment applies the same authoritative state transition as
// /EquipBatchUse without fabricating a second wire protocol implementation.
func (s *EquipmentInventory) ApplyPresetEquipment(bindings []PresetEquipmentBinding) ([]Character, error) {
	if err := s.ValidatePresetEquipment(bindings); err != nil {
		return nil, err
	}
	// PresetUse is a server-side atomic operation. Unlike the client-orchestrated
	// EquipPreset UI, it must also clear any previous owner of an equipment
	// instance in the same batch.
	s.mu.Lock()
	requested := make(map[uint64]bool, len(bindings))
	desired := make(map[uint64]bool)
	for _, binding := range bindings {
		requested[binding.CharacterIndex] = true
		for _, index := range binding.Equipment {
			desired[index] = index != 0
		}
	}
	additional := make(map[uint64][]uint64)
	for _, item := range s.owned.Equipment {
		if item.UseChar == 0 || requested[item.UseChar] || !desired[item.InvenIndex] {
			continue
		}
		if additional[item.UseChar] == nil {
			additional[item.UseChar] = make([]uint64, equipmentSlotCount)
			for _, equipped := range s.owned.Equipment {
				if equipped.UseChar == item.UseChar {
					if slot, ok := s.slots[equipped.ID]; ok && slot < equipmentSlotCount {
						additional[item.UseChar][slot] = equipped.InvenIndex
					}
				}
			}
		}
		if slot, ok := s.slots[item.ID]; ok && slot < equipmentSlotCount {
			additional[item.UseChar][slot] = 0
		}
	}
	s.mu.Unlock()
	for character, equipment := range additional {
		bindings = append(bindings, PresetEquipmentBinding{CharacterIndex: character, Equipment: equipment})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].CharacterIndex < bindings[j].CharacterIndex })
	request := wire.AppendVarint(nil, 1, 1)
	for _, binding := range bindings {
		entry := wire.AppendVarint(nil, 1, binding.CharacterIndex)
		for _, index := range binding.Equipment {
			entry = wire.AppendVarint(entry, 2, index)
		}
		request = wire.AppendBytes(request, 2, entry)
	}
	_, response, handled, err := s.batchUse(request)
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, errors.New("player: preset equipment batch was not handled")
	}
	var result []Character
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number != 1 || field.Type != 2 {
			return nil
		}
		index, found, err := wire.Varint(field.Value, 1)
		if err != nil || !found || index == 0 {
			return errors.New("player: malformed preset equipment character response")
		}
		character, found := s.characters.Find(index)
		if !found {
			return fmt.Errorf("player: preset equipment character %d disappeared", index)
		}
		result = append(result, character)
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

// ApplyPresetCostumes changes the currently selected costume for each listed
// character and persists both seeded and collection-owned character records.
func (s *CharacterStore) ApplyPresetCostumes(assignments map[uint64]uint64) ([]Character, error) {
	if len(assignments) == 0 {
		return nil, errors.New("player: empty preset costume assignments")
	}
	s.mu.Lock()
	base := append([]Character(nil), s.characters...)
	collection := s.collection
	s.mu.Unlock()
	if collection == nil {
		return nil, errors.New("player: preset costumes require collection store")
	}
	collectionCharacters := collection.Characters()
	seenCostumes := make(map[uint64]bool)
	updated := make(map[uint64]Character, len(assignments))
	for characterIndex, costumeIndex := range assignments {
		var current Character
		found := false
		for _, character := range base {
			if character.InvenIndex == characterIndex {
				current, found = character, true
				break
			}
		}
		if !found {
			for _, character := range collectionCharacters {
				if character.InvenIndex == characterIndex {
					current, found = character, true
					break
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("player: preset references unknown character %d", characterIndex)
		}
		if costumeIndex == 0 {
			current.CostumeID = 0
			current.UseCostume = 0
		} else {
			if seenCostumes[costumeIndex] {
				return nil, fmt.Errorf("player: preset repeats costume %d", costumeIndex)
			}
			costume, found := collection.CostumeByIndex(costumeIndex)
			if !found {
				return nil, fmt.Errorf("player: preset references unknown costume %d", costumeIndex)
			}
			seenCostumes[costumeIndex] = true
			current.CostumeID = costume.ID
			current.UseCostume = costumeIndex
		}
		updated[characterIndex] = current
	}

	nextBase := append([]Character(nil), base...)
	baseChanged := false
	for i := range nextBase {
		if character, ok := updated[nextBase[i].InvenIndex]; ok {
			nextBase[i] = character
			delete(updated, character.InvenIndex)
			baseChanged = true
		}
	}
	if baseChanged {
		s.mu.Lock()
		if err := s.persist(nextBase); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.characters = nextBase
		s.mu.Unlock()
	}
	for index, character := range updated {
		old, found := collection.FindCharacter(index)
		if !found {
			return nil, fmt.Errorf("player: preset collection character %d disappeared", index)
		}
		if err := collection.UpdateCharacter(old.ID, character); err != nil {
			return nil, err
		}
	}
	indices := make([]uint64, 0, len(assignments))
	for index := range assignments {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	result := make([]Character, 0, len(indices))
	for _, index := range indices {
		character, found := s.Find(index)
		if !found {
			return nil, fmt.Errorf("player: preset character %d disappeared after update", index)
		}
		result = append(result, character)
	}
	return result, nil
}
