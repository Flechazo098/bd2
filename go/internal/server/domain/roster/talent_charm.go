package roster

import (
	"bd2server/internal/server/domain/command"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

const CharmCharacterIndexBase uint64 = 1 << 59

func IsCharmCharacter(c Character) bool {
	return c.InvenIndex >= CharmCharacterIndexBase && c.InvenIndex < StoryCharacterIndexBase
}
func CharacterExpired(c Character, now time.Time) bool {
	return c.ExpiryTime > 0 && c.ExpiryTime <= uint64(now.UnixMilli())
}

func (s *CharacterStore) ensureCharmCharacters(ctx command.Context, chars []Character) error {

	next := append([]Character(nil), s.characters...)
	for _, c := range chars {
		if !IsCharmCharacter(c) || c.ExpiryTime == 0 {
			return fmt.Errorf("player: invalid charm instance")
		}
		found := false
		for i, old := range next {
			if old.InvenIndex == c.InvenIndex {
				if old.ID != c.ID {
					return fmt.Errorf("player: charm instance collision")
				}
				next[i] = c
				found = true
				break
			}
		}
		if !found {
			next = append(next, c)
		}
	}
	if err := s.persist(ctx, next); err != nil {
		return err
	}
	s.characters = next
	for _, c := range chars {
		raw, err := json.Marshal(c.HP)
		if err != nil {
			return err
		}
		if err = s.store.PutEntry(ctx.State, "characters", "current_hp", strconv.FormatUint(c.InvenIndex, 10), raw); err != nil {
			return err
		}
	}
	return nil
}
