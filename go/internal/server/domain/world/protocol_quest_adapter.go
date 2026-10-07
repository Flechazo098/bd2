package world

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"sort"
)

// QuestPacket always passes the repeated DeckInfo to CommonPacket.RefreshDeck,
// which replaces the entire battle deck even when that list is empty. Keep
// this live snapshot outside the commission reward receipt: retrying a clear
// after the player changes formation must return the current saved deck.
func (s *Service) commissionResponseDeck(code int, response []byte) []byte {
	field := 0
	switch code {
	case 17: // QuestAcceptResponse.deck_info
		field = 3
	case 18: // QuestClearResponse.deck_info
		field = 4
	case 20: // QuestGiveUpResponse.deck_info
		field = 2
	}
	if field != 0 {
		return s.appendCurrentBattleDeck(response, field)
	}
	return response
}

func (s *Service) currentBattleDeckWires() [][]byte {
	if s.decks == nil {
		return nil
	}
	var entries [][]byte
	for _, current := range s.decks.CurrentDeck() {
		entry := wire.AppendVarint(nil, 1, current.CharacterInvenIndex)
		// DeckDBInfo field 2 is the battle-grid position, including zero and -1.
		entry = wire.AppendVarint(entry, 2, current.CostumeInvenIndex)
		entry = wire.AppendVarint(entry, 3, current.Slot)
		entries = append(entries, entry)
	}
	return entries
}

func (s *Service) appendCurrentBattleDeck(response []byte, field int) []byte {
	for _, entry := range s.currentBattleDeckWires() {
		response = wire.AppendBytes(response, field, entry)
	}
	return response
}

func (s *Service) questInfoWire(packID, questID int) []byte {
	out := wire.AppendVarint(nil, 1, uint64(questID))
	selection, _ := s.state.Selection(packID)
	if s.storyCatalog != nil && s.storyCatalog.Packs[packID].Quests[questID].Type != 0 {
		selection = progress.QuestSelection{}
	}
	if current, ok := s.state.QuestInPack(questID, packID, selection.Difficulty); ok {
		for _, value := range current.Values {
			out = wire.AppendVarint(out, 3, uint64(value))
		}
	}
	if selection.Difficulty != 0 {
		out = wire.AppendVarint(out, 4, uint64(selection.Difficulty))
	}
	if selection.Option != 0 {
		out = wire.AppendVarint(out, 5, uint64(selection.Option))
	}
	return wire.AppendVarint(out, 6, uint64(packID))
}

func (s *Service) questLevelInfoWire(packID, difficulty int) []byte {
	out := wire.AppendVarint(nil, 1, uint64(packID))
	out = wire.AppendVarint(out, 2, uint64(difficulty))
	last := 0
	complete := true
	pack := s.storyCatalog.Packs[packID]
	for _, id := range pack.MainQuestIDs {
		if !s.state.QuestCleared(id, packID, difficulty) {
			complete = false
			break
		}
		last = id
	}
	out = wire.AppendVarint(out, 3, uint64(last))
	selection, _ := s.state.Selection(packID)
	if selection.Difficulty == difficulty {
		out = wire.AppendVarint(out, 4, uint64(selection.Option))
	}
	if complete && len(pack.MainQuestIDs) > 0 {
		out = wire.AppendVarint(out, 5, 1)
	}
	return out
}

// Pack entry replaces the client's completed-ID list. Side quests always use
// normal progress, independently of the selected main-story difficulty.
func (s *Service) completedQuestIDs(pack int) []int {
	ids := s.state.ClearedQuests(pack, s.questDifficulty(pack))
	if s.storyCatalog == nil || s.questDifficulty(pack) == 0 {
		return ids
	}
	for _, id := range s.state.ClearedQuests(pack, 0) {
		if s.storyCatalog.Packs[pack].Quests[id].Type == 1 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids
}

func (s *Service) handleQuestSelection(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/QuestInfo" && s.battleActive != nil && s.battleActive(ctx) {
		return 0, nil, true, fmt.Errorf("%w: active battle", ErrInvalidRequest)
	}
	if path == "/QuestInfo" {
		pack, err := requestPack(request)
		if err != nil || !s.packUnlocked(ctx, pack) {
			return 0, nil, true, ErrInvalidRequest
		}
		var out []byte
		if active := s.firstUnclearedQuestFor(pack); active != 0 {
			out = wire.AppendBytes(out, 1, s.questInfoWire(pack, active))
		}
		for _, quest := range s.activeSideQuestWires(pack) {
			out = wire.AppendBytes(out, 1, quest)
		}
		var todayCleared []int
		if s.todayQuests != nil {
			rows, ids, err := s.todayQuests.Info(ctx, pack)
			if err != nil {
				return 0, nil, true, err
			}
			todayCleared = ids
			for _, row := range rows {
				out = wire.AppendBytes(out, 1, row)
			}
		}
		var cleared []byte
		for _, id := range s.completedQuestIDs(pack) {
			cleared = binary.AppendUvarint(cleared, uint64(id))
		}
		for _, id := range todayCleared {
			cleared = binary.AppendUvarint(cleared, uint64(id))
		}
		if len(cleared) > 0 {
			out = wire.AppendBytes(out, 2, cleared)
		}
		return 16, out, true, nil
	}
	quest, pack, err := requestQuest(request)
	if err != nil || !s.packUnlocked(ctx, pack) {
		return 0, nil, true, ErrInvalidRequest
	}
	designs, known := s.questsFor(ctx, pack)
	design, exists := designs[quest]
	if !known || !exists {
		return 0, nil, true, ErrInvalidRequest
	}
	if path == "/QuestGiveUp" {
		if design.Type != 0 {
			if _, ok := s.state.QuestInPack(quest, pack); !ok {
				return 0, nil, true, ErrInvalidRequest
			}
			if err := s.state.RemoveQuest(ctx, quest, pack, 0); err != nil {
				return 0, nil, true, err
			}
			return 20, s.appendCurrentBattleDeck(wire.AppendVarint(nil, 1, uint64(quest)), 2), true, nil
		}
		selection, ok := s.state.Selection(pack)
		if !ok || selection.QuestID != quest || selection.Difficulty == 0 {
			return 0, nil, true, ErrInvalidRequest
		}
		// Abandoning a difficulty retains its independently committed clears.
		if err := s.state.SelectQuest(ctx, pack, progress.QuestSelection{}); err != nil {
			return 0, nil, true, err
		}
		return 20, s.appendCurrentBattleDeck(wire.AppendVarint(nil, 1, uint64(quest)), 2), true, nil
	}
	level, _, err := wire.Varint(request, 4)
	if err != nil || level > 4 {
		return 0, nil, true, ErrInvalidRequest
	}
	opt, _, err := wire.Varint(request, 5)
	if err != nil || opt != 0 {
		return 0, nil, true, ErrInvalidRequest
	}
	if level > 0 && !s.questDifficulties[pack][int(level)] {
		return 0, nil, true, ErrInvalidRequest
	}
	if design.Type != 0 {
		if design.Type != 1 {
			return 0, nil, true, ErrInvalidRequest
		}
		if level != 0 || (design.PriorQuestID != 0 && !s.state.QuestCleared(design.PriorQuestID, pack)) || s.state.QuestCleared(quest, pack) {
			return 0, nil, true, ErrInvalidRequest
		}
		if err := s.state.AcceptQuest(ctx, quest, pack, 0); err != nil {
			return 0, nil, true, err
		}
		out := wire.AppendBytes(nil, 1, s.questInfoWire(pack, quest))
		out = s.appendCurrentBattleDeck(out, 3)
		items, err := s.ensureQuestItems(ctx, pack, quest)
		if err != nil {
			return 0, nil, true, err
		}
		for _, item := range items {
			out = wire.AppendBytes(out, 4, assets.ItemWire(item))
		}
		return 17, out, true, nil
	}
	if level > 0 {
		ids := s.storyCatalog.Packs[pack].MainQuestIDs
		for _, id := range ids {
			if !s.state.QuestCleared(id, pack, int(level)-1) {
				return 0, nil, true, fmt.Errorf("%w: previous difficulty is incomplete", ErrInvalidRequest)
			}
		}
	}
	// Accept only the first unfinished main quest in this difficulty, using
	// authoritative links rather than permitting an arbitrary reward jump.
	first := 0
	for _, id := range s.storyCatalog.Packs[pack].MainQuestIDs {
		if !s.state.QuestCleared(id, pack, int(level)) {
			first = id
			break
		}
	}
	if quest != first || first == 0 {
		return 0, nil, true, ErrInvalidRequest
	}
	if err := s.state.SelectQuest(ctx, pack, progress.QuestSelection{QuestID: quest, Difficulty: int(level), Option: int(opt)}); err != nil {
		return 0, nil, true, err
	}
	if err := s.state.SetActivePackID(ctx, pack); err != nil {
		return 0, nil, true, err
	}
	s.setCurrentPack(pack)
	chars, decks, err := s.resolveActivePartyWires(ctx, pack, quest)
	if err != nil {
		return 0, nil, true, err
	}
	out := wire.AppendBytes(nil, 1, s.questInfoWire(pack, quest))
	for _, char := range chars {
		out = wire.AppendBytes(out, 2, char)
	}
	for _, deck := range decks {
		out = wire.AppendBytes(out, 3, deck)
	}
	items, err := s.ensureQuestItems(ctx, pack, quest)
	if err != nil {
		return 0, nil, true, err
	}
	for _, item := range items {
		out = wire.AppendBytes(out, 4, assets.ItemWire(item))
	}
	return 17, out, true, nil
}

func (s *Service) activeSideQuestWires(packID int) [][]byte {
	var out [][]byte
	if s.storyCatalog == nil {
		return nil
	}
	for _, quest := range s.state.QuestsInPack(packID, 0) {
		if s.storyCatalog.Packs[packID].Quests[quest.QuestID].Type == 1 && !s.state.QuestCleared(quest.QuestID, packID) {
			out = append(out, s.questInfoWire(packID, quest.QuestID))
		}
	}
	return out
}
