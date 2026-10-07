package deck

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
)

func (s *Store) CurrentFieldDeck(ctx command.Context) []FieldEntry {

	return s.visibleFieldDeckLocked(ctx)
}

func (s *Store) visibleFieldDeckLocked(ctx command.Context) []FieldEntry {
	var out []FieldEntry
	for _, entry := range s.state.FieldDeck {
		if s.characters != nil {
			c, ok := s.characters.Find(ctx, entry.CharacterInvenIndex)
			if !ok || roster.IsStoryCharacter(c) && !s.temporaryAllowed(ctx, c) {
				continue
			}
		}
		entry.Slot = uint64(len(out) + 1)
		out = append(out, entry)
	}
	return out
}
