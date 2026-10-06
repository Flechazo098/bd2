package deck

import "bd2server/internal/server/player"

func (s *Store) CurrentFieldDeck() []FieldEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.visibleFieldDeckLocked()
}

func (s *Store) visibleFieldDeckLocked() []FieldEntry {
	var out []FieldEntry
	for _, entry := range s.state.FieldDeck {
		if s.characters != nil {
			c, ok := s.characters.Find(entry.CharacterInvenIndex)
			if !ok || player.IsStoryCharacter(c) && !s.temporaryAllowed(c) {
				continue
			}
		}
		entry.Slot = uint64(len(out) + 1)
		out = append(out, entry)
	}
	return out
}
