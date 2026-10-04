package player

import "fmt"

// StoryCharacterIndexBase reserves local instances for temporary quest parties.
// These are not collection rewards and never receive a collection grant.
const StoryCharacterIndexBase uint64 = 1 << 60

func IsStoryCharacter(c Character) bool { return c.InvenIndex >= StoryCharacterIndexBase }

// EnsureStoryCharacters saves new authored temporary characters atomically.
// Existing instances retain their completed-battle health across reconnects.
func (s *CharacterStore) EnsureStoryCharacters(characters []Character) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]Character(nil), s.characters...)
	seen := map[uint64]Character{}
	for _, c := range next {
		seen[c.InvenIndex] = c
	}
	for _, c := range characters {
		if !IsStoryCharacter(c) || c.ID == 0 || c.Level == 0 || c.CostumeID == 0 {
			return fmt.Errorf("player: invalid temporary story character")
		}
		if old, ok := seen[c.InvenIndex]; ok {
			if old.ID != c.ID || old.Level != c.Level || old.CostumeID != c.CostumeID {
				return fmt.Errorf("player: story instance %d design collision", c.InvenIndex)
			}
			continue
		}
		next = append(next, c)
		seen[c.InvenIndex] = c
	}
	if err := s.persist(next); err != nil {
		return err
	}
	s.characters = next
	return nil
}
