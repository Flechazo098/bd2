package world

import (
	"bd2server/internal/server/deck"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
)

// A normal battle deck has five character slots (CommonPacket and DeckSave).
const storyBattlePartySize = 5

// ResolveStoryParty applies the authored temporary CharGroup members to the
// saved battle party. StoryCharGroup costume designs only describe the field
// story cast: FieldDeckPacket builds their cosmetic actors without inventory
// identities, so they do not reserve battle slots, including placeholders.
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
	var authoredOrder []uint64
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
			authoredOrder = append(authoredOrder, existing.InvenIndex)
			continue
		}
		if packID <= 0 || packID >= 65536 || d.CharacterID == 0 || d.CharacterID >= 1<<32 || d.Level == 0 || d.Level >= 256 {
			return nil, fmt.Errorf("world: story instance namespace overflow")
		}
		index := player.StoryCharacterIndexBase | uint64(packID)<<40 | d.CharacterID<<8 | d.Level
		authoredOrder = append(authoredOrder, index)
		temporary = append(temporary, player.Character{InvenIndex: index, ID: d.CharacterID, HP: d.HP, Level: d.Level, CostumeID: d.CostumeID, TalentLevel: d.InitialTalentLevel})
	}
	if len(designs) > storyBattlePartySize {
		return nil, fmt.Errorf("world: pack%d quest%d character group%d exceeds battle capacity: %d", packID, questID, formation.CharGroupID, len(designs))
	}
	if err := s.characters.EnsureStoryCharacters(temporary); err != nil {
		return nil, err
	}
	authored := make(map[uint64]player.Character, len(designs))
	for _, c := range reused {
		current, ok := s.characters.Find(c.InvenIndex)
		if !ok {
			return nil, fmt.Errorf("world: saved story instance unavailable")
		}
		authored[current.InvenIndex] = current
	}
	for _, c := range temporary {
		current, ok := s.characters.Find(c.InvenIndex)
		if !ok {
			return nil, fmt.Errorf("world: story character was not saved")
		}
		authored[current.InvenIndex] = current
	}
	owned := map[uint64]player.Character{}
	for _, c := range s.visibleOwnedCharacters(s.characters.All()) {
		banned := false
		for _, row := range formation.Characters {
			// PackManager.CalcBanCharUniqueId groups growth rows by ID/10.
			if row.Banned && row.CharacterID/10 == c.ID/10 {
				banned = true
				break
			}
		}
		if banned {
			continue
		}
		owned[c.InvenIndex] = c
	}
	var candidates []uint64
	if s.decks != nil {
		for _, entry := range s.decks.CurrentDeck() {
			candidates = append(candidates, entry.CharacterInvenIndex)
		}
		// With no saved battle choice, the account's field party supplies its
		// initial controlled members. A saved battle choice takes precedence.
		if len(candidates) == 0 {
			for _, entry := range s.decks.CurrentFieldDeck() {
				candidates = append(candidates, entry.CharacterInvenIndex)
			}
		}
	}
	result := make([]player.Character, 0, storyBattlePartySize)
	used := map[uint64]bool{}
	controlledSlots := storyBattlePartySize - len(authored)
	for _, index := range candidates {
		if used[index] {
			continue
		}
		if c, ok := authored[index]; ok {
			result = append(result, c)
			used[index] = true
			continue
		}
		if c, ok := owned[index]; ok && controlledSlots > 0 {
			result = append(result, c)
			used[index] = true
			controlledSlots--
		}
	}
	for _, index := range authoredOrder {
		if !used[index] {
			result = append(result, authored[index])
			used[index] = true
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
	savedPositions := map[uint64]uint64{}
	if s.decks != nil {
		for _, entry := range s.decks.CurrentDeck() {
			savedPositions[entry.CharacterInvenIndex] = entry.CostumeInvenIndex
		}
	}
	for i, c := range party {
		position := ^uint64(0)
		if i < len(formation.DeckList) {
			position = formation.DeckList[i]
		} else if len(formation.DeckList) == 0 {
			if saved, ok := savedPositions[c.InvenIndex]; ok {
				position = saved
			}
		}
		entry := deck.DeckEntry{CharacterInvenIndex: c.InvenIndex, CostumeInvenIndex: position, Slot: uint64(i + 1)}
		entries = append(entries, entry)
		characters = append(characters, encodeCharacter(c))
		data := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, c.InvenIndex), 2, position), 3, uint64(i+1))
		wires = append(wires, data)
	}
	if s.decks != nil {
		if err := s.decks.SetStoryParty(entries); err != nil {
			return nil, nil, fmt.Errorf("world: save battle party pack%d quest%d charGroup%d storyCharGroup%d members%d: %w", packID, questID, formation.CharGroupID, formation.StoryCharGroupID, len(entries), err)
		}
	}
	return characters, wires, nil
}
