package world

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"fmt"
)

// currentBattleParty reads the player's chosen battle party without changing
// membership or positions. Ordinary story quests do not force their temporary
// guests into battle; fixed-character modes own their separate battle decks.
func (s *Service) currentBattleParty(ctx command.Context) ([]roster.Character, error) {
	if s.decks == nil {
		return nil, nil
	}
	var party []roster.Character
	for _, entry := range s.decks.CurrentDeck() {
		if s.characters == nil {
			return nil, fmt.Errorf("world: battle character store unavailable")
		}
		c, found := s.characters.Find(ctx, entry.CharacterInvenIndex)
		if !found {
			return nil, fmt.Errorf("world: saved battle character %d unavailable", entry.CharacterInvenIndex)
		}
		party = append(party, c)
	}
	return party, nil
}

// resolveStoryCharacters provisions authored temporary guests for the pack's
// character and talent UI. Availability is independent of battle selection;
// StoryCharGroup supplies cosmetic field actors, not inventory identities.
func (s *Service) resolveStoryCharacters(ctx command.Context, packID, questID int) ([]roster.Character, error) {
	if s.storyRoster == nil {
		return nil, nil
	}
	if s.characters == nil {
		return nil, fmt.Errorf("world: story character store unavailable")
	}
	_, ok := s.storyRoster.Formation(packID, questID)
	if !ok {
		return nil, fmt.Errorf("world: missing formation pack%d quest%d", packID, questID)
	}
	designs, err := s.storyRoster.Characters(packID, questID)
	if err != nil {
		return nil, err
	}
	temporary := make([]roster.Character, 0, len(designs))
	reused := make([]roster.Character, 0, len(designs))
	var authoredOrder []uint64
	for _, d := range designs {
		var existing roster.Character
		for _, c := range s.characters.RawAll() {
			if d.TemporaryPack != 0 && c.ID == d.CharacterID && c.Level == d.Level && (c.CostumeID == 0 || c.CostumeID == d.CostumeID) {
				existing = c
				break
			}
		}
		if existing.InvenIndex != 0 {
			reused = append(reused, existing)
			authoredOrder = append(authoredOrder, existing.InvenIndex)
			continue
		}
		if packID <= 0 || packID >= 65536 || d.CharacterID == 0 || d.CharacterID >= 1<<32 || d.Level == 0 || d.Level >= 256 {
			return nil, fmt.Errorf("world: story instance namespace overflow")
		}
		index := roster.StoryCharacterIndexBase | uint64(packID)<<40 | d.CharacterID<<8 | d.Level
		authoredOrder = append(authoredOrder, index)
		temporary = append(temporary, roster.Character{InvenIndex: index, ID: d.CharacterID, HP: d.HP, Level: d.Level, CostumeID: d.CostumeID, TalentLevel: d.InitialTalentLevel})
	}
	if err := s.characters.EnsureStoryCharacters(ctx, temporary); err != nil {
		return nil, err
	}
	authored := make(map[uint64]roster.Character, len(designs))
	for _, c := range reused {
		current, ok := s.characters.Find(ctx, c.InvenIndex)
		if !ok {
			return nil, fmt.Errorf("world: saved story instance unavailable")
		}
		authored[current.InvenIndex] = current
	}
	for _, c := range temporary {
		current, ok := s.characters.Find(ctx, c.InvenIndex)
		if !ok {
			return nil, fmt.Errorf("world: story character was not saved")
		}
		authored[current.InvenIndex] = current
	}
	result := make([]roster.Character, 0, len(authoredOrder))
	for _, index := range authoredOrder {
		result = append(result, authored[index])
	}
	return result, nil
}
