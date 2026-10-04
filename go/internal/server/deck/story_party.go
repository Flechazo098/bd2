package deck

func (s *Store) CurrentFieldDeck() []FieldEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]FieldEntry(nil), s.state.FieldDeck...)
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
