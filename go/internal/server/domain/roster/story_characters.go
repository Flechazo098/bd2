package roster

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
)

// StoryCharacterIndexBase marks temporary quest parties, not collection rewards.
const StoryCharacterIndexBase uint64 = 1 << 60

func (s *CharacterStore) StoryCharacterIndex(ctx command.Context, pack, character, level uint64) (uint64, error) {
	if pack == 0 || character == 0 || level == 0 {
		return 0, fmt.Errorf("player: invalid story character identity")
	}
	return s.temporaryCharacterIndex(ctx, StoryCharacterIndexBase, fmt.Sprintf("story:%d:%d:%d", pack, character, level))
}

func (s *CharacterStore) CharmCharacterIndex(ctx command.Context, pack, character uint64) (uint64, error) {
	if pack == 0 || character == 0 {
		return 0, fmt.Errorf("player: invalid charm character identity")
	}
	return s.temporaryCharacterIndex(ctx, CharmCharacterIndexBase, fmt.Sprintf("charm:%d:%d", pack, character))
}

func (s *CharacterStore) temporaryCharacterIndex(ctx command.Context, namespace uint64, key string) (uint64, error) {
	raw, found, err := s.store.LoadEntry(ctx.State, "characters", "temporary_instances", key)
	if err != nil {
		return 0, err
	}
	var index uint64
	if found {
		if json.Unmarshal(raw, &index) != nil || index == 0 || index >= CharmCharacterIndexBase {
			return 0, fmt.Errorf("player: invalid temporary instance identity %q", key)
		}
	} else {
		index, err = ctx.NewInstanceID()
		if err != nil {
			return 0, err
		}
		payload, err := json.Marshal(index)
		if err != nil {
			return 0, err
		}
		if err := s.store.SaveWithEntries(ctx.State, "characters", nil, []stateio.EntryMutation{{Bucket: "temporary_instances", Key: key, Payload: payload}}); err != nil {
			return 0, err
		}
	}
	return namespace | index, nil
}

func IsStoryCharacter(c Character) bool { return c.InvenIndex >= StoryCharacterIndexBase }

// EnsureStoryCharacters saves new authored temporary characters atomically.
// Existing instances retain their completed-battle health across reconnects.
func (s *CharacterStore) EnsureStoryCharacters(ctx command.Context, characters []Character) error {

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
	if err := s.persist(ctx, next); err != nil {
		return err
	}
	s.characters = next
	return nil
}
