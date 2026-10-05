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

// SetStoryParty stores the authored active quest formation using the same
// character ownership checks as DeckSave. Empty formation keeps player choice.
func (s *Store) SetStoryParty(entries []DeckEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if err := validDeck(entries); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateOwnedDeckLocked(entries); err != nil {
		return err
	}
	next := clone(s.state)
	next.Deck = append([]DeckEntry(nil), entries...)
	return s.commit(next)
}
