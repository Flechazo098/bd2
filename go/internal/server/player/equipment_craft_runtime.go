package player

import (
	"errors"
	"fmt"
	"math"
)

// AddTalentExperience applies the same clamp as the client: talent experience
// may reach the cumulative threshold of the current level, but this request
// never promotes the talent level itself.
func (s *CharacterStore) AddTalentExperience(index, gain, maximum uint64) (Character, error) {
	if s == nil || index == 0 || maximum == 0 {
		return Character{}, errors.New("player: invalid talent experience update")
	}
	s.mu.Lock()
	for position, current := range s.characters {
		if current.InvenIndex != index {
			continue
		}
		if current.TalentExp > maximum {
			s.mu.Unlock()
			return Character{}, fmt.Errorf("player: character %d talent experience exceeds current threshold", index)
		}
		if gain > maximum-current.TalentExp {
			gain = maximum - current.TalentExp
		}
		if current.TalentExp > math.MaxUint64-gain {
			s.mu.Unlock()
			return Character{}, errors.New("player: talent experience overflow")
		}
		current.TalentExp += gain
		next := append([]Character(nil), s.characters...)
		next[position] = current
		if err := s.persist(next); err != nil {
			s.mu.Unlock()
			return Character{}, fmt.Errorf("player: persist equipment making talent experience: %w", err)
		}
		s.characters = next
		s.mu.Unlock()
		return current, nil
	}
	collection := s.collection
	s.mu.Unlock()
	if collection == nil {
		return Character{}, fmt.Errorf("player: unknown equipment making character %d", index)
	}
	current, found := collection.FindCharacter(index)
	if !found {
		return Character{}, fmt.Errorf("player: unknown equipment making character %d", index)
	}
	if current.TalentExp > maximum {
		return Character{}, fmt.Errorf("player: character %d talent experience exceeds current threshold", index)
	}
	if gain > maximum-current.TalentExp {
		gain = maximum - current.TalentExp
	}
	if current.TalentExp > math.MaxUint64-gain {
		return Character{}, errors.New("player: talent experience overflow")
	}
	current.TalentExp += gain
	if err := collection.UpdateCharacter(current.ID, current); err != nil {
		return Character{}, fmt.Errorf("player: persist collection equipment making talent experience: %w", err)
	}
	return current, nil
}
