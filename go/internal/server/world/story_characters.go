package world

import (
	"bd2server/internal/server/deck"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
)

// ResolveStoryParty combines the authored temporary CharGroup members with
// actual account instances named by StoryCharGroup costume rows.
func (s *Service) ResolveStoryParty(packID, questID int) ([]player.Character, error) {
	if s.storyRoster == nil {
		return nil, nil
	}
	if s.characters == nil {
		return nil, fmt.Errorf("world: story character store unavailable")
	}
	formation, ok := s.storyRoster.Formation(packID, questID)
	if !ok {
		return nil, fmt.Errorf("world: missing formation pack%d quest%d", packID, questID)
	}
	designs, err := s.storyRoster.Characters(packID, questID)
	if err != nil {
		return nil, err
	}
	temporary := make([]player.Character, 0, len(designs))
	reused := make([]player.Character, 0, len(designs))
	for _, d := range designs {
		var existing player.Character
		for _, c := range s.characters.RawAll() {
			if d.TemporaryPack != 0 && c.ID == d.CharacterID && c.Level == d.Level && (c.CostumeID == 0 || c.CostumeID == d.CostumeID) {
				existing = c
				break
			}
		}
		if existing.InvenIndex != 0 {
			reused = append(reused, existing)
			continue
		}
		if packID <= 0 || packID >= 65536 || d.CharacterID == 0 || d.CharacterID >= 1<<32 || d.Level == 0 || d.Level >= 256 {
			return nil, fmt.Errorf("world: story instance namespace overflow")
		}
		index := player.StoryCharacterIndexBase | uint64(packID)<<40 | d.CharacterID<<8 | d.Level
		temporary = append(temporary, player.Character{InvenIndex: index, ID: d.CharacterID, HP: d.HP, Level: d.Level, CostumeID: d.CostumeID, TalentLevel: d.InitialTalentLevel})
	}
	if s.characters == nil {
		return nil, fmt.Errorf("world: story character store unavailable")
	}
	if err := s.characters.EnsureStoryCharacters(temporary); err != nil {
		return nil, err
	}
	result := make([]player.Character, 0, len(temporary)+len(formation.StoryCostumes))
	used := map[uint64]bool{}
	for _, c := range reused {
		current, ok := s.characters.Find(c.InvenIndex)
		if !ok {
			return nil, fmt.Errorf("world: saved story instance unavailable")
		}
		result = append(result, current)
		used[current.InvenIndex] = true
	}
	for _, c := range temporary {
		current, ok := s.characters.Find(c.InvenIndex)
		if !ok {
			return nil, fmt.Errorf("world: story character was not saved")
		}
		result = append(result, current)
		used[current.InvenIndex] = true
	}
	// Placeholder entries are player-controlled party slots. Their costume
	// and character cannot be inferred from the design placeholder itself.
	for _, costume := range formation.StoryCostumes {
		if s.seed.PlaceholderCostumeID != 0 && costume.CostumeID == s.seed.PlaceholderCostumeID {
			continue
		}
		temporaryMember := false
		for _, design := range designs {
			if design.UniqueCharacterID == costume.UniqueCharacterID {
				temporaryMember = true
				break
			}
		}
		if temporaryMember {
			continue
		}
		var ownedCostumes []player.Costume
		if s.starter != nil {
			ownedCostumes = append(ownedCostumes, s.starter.Costumes...)
		}
		if s.collection != nil {
			ownedCostumes = append(ownedCostumes, s.collection.Costumes()...)
		}
		for _, c := range s.visibleOwnedCharacters(s.characters.All()) {
			matched := c.CostumeID == costume.CostumeID
			for _, owned := range ownedCostumes {
				if owned.ID == costume.CostumeID && owned.UseChar == c.InvenIndex {
					matched = true
					c.CostumeID = owned.ID
					c.UseCostume = owned.InvenIndex
					break
				}
			}
			if player.IsStoryCharacter(c) || used[c.InvenIndex] || !matched {
				continue
			}
			result = append(result, c)
			used[c.InvenIndex] = true
			break
		}
	}
	// Fill authored player-controlled slots from the persisted field party,
	// then the persisted battle party. Never synthesize the placeholder ID.
	for _, costume := range formation.StoryCostumes {
		if (s.seed.PlaceholderCostumeID == 0 || costume.CostumeID != s.seed.PlaceholderCostumeID) || s.decks == nil {
			continue
		}
		var candidates []uint64
		for _, entry := range s.decks.CurrentFieldDeck() {
			candidates = append(candidates, entry.CharacterInvenIndex)
		}
		for _, entry := range s.decks.CurrentDeck() {
			candidates = append(candidates, entry.CharacterInvenIndex)
		}
		for _, index := range candidates {
			if used[index] {
				continue
			}
			var controlled player.Character
			for _, c := range s.visibleOwnedCharacters(s.characters.All()) {
				if c.InvenIndex == index {
					controlled = c
					break
				}
			}
			if controlled.InvenIndex == 0 {
				continue
			}
			temporaryDesign := false
			for _, d := range designs {
				if d.CharacterID == controlled.ID {
					temporaryDesign = true
					break
				}
			}
			if temporaryDesign {
				continue
			}
			result = append(result, controlled)
			used[index] = true
			break
		}
	}
	return result, nil
}

func (s *Service) resolveActivePartyWires(packID, questID int) ([][]byte, [][]byte, error) {
	party, err := s.ResolveStoryParty(packID, questID)
	if err != nil {
		return nil, nil, err
	}
	if len(party) == 0 {
		return nil, nil, nil
	}
	formation, _ := s.storyRoster.Formation(packID, questID)
	entries := make([]deck.DeckEntry, 0, len(party))
	characters := make([][]byte, 0, len(party))
	wires := make([][]byte, 0, len(party))
	for i, c := range party {
		position := ^uint64(0)
		if i < len(formation.DeckList) {
			position = formation.DeckList[i]
		}
		entry := deck.DeckEntry{CharacterInvenIndex: c.InvenIndex, CostumeInvenIndex: position, Slot: uint64(i + 1)}
		entries = append(entries, entry)
		characters = append(characters, encodeCharacter(c))
		data := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, c.InvenIndex), 2, position), 3, uint64(i+1))
		wires = append(wires, data)
	}
	if s.decks != nil {
		if err := s.decks.SetStoryParty(entries); err != nil {
			return nil, nil, err
		}
	}
	return characters, wires, nil
}
