package roster

import (
	"bd2server/internal/server/domain/command"
	"errors"
	"fmt"
	"slices"
)

func (s *CharacterStore) ApplyPresetCostumes(ctx command.Context, assignments map[uint64]uint64) ([]Character, error) {
	if len(assignments) == 0 {
		return nil, errors.New("player: empty preset costume assignments")
	}

	base := append([]Character(nil), s.characters...)
	collection := s.collection

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

		if err := s.persist(ctx, nextBase); err != nil {

			return nil, err
		}
		s.characters = nextBase

	}
	for index, character := range updated {
		old, found := collection.FindCharacter(index)
		if !found {
			return nil, fmt.Errorf("player: preset collection character %d disappeared", index)
		}
		if err := collection.UpdateCharacter(ctx, old.ID, character); err != nil {
			return nil, err
		}
	}
	indices := make([]uint64, 0, len(assignments))
	for index := range assignments {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	result := make([]Character, 0, len(indices))
	for _, index := range indices {
		character, found := s.Find(ctx, index)
		if !found {
			return nil, fmt.Errorf("player: preset character %d disappeared after update", index)
		}
		result = append(result, character)
	}
	return result, nil
}
