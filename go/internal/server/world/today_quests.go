package world

import (
	"bd2server/internal/server/todayquest"
	"bd2server/internal/server/wire"
	"fmt"
)

func (s *Service) AttachTodayQuests(service *todayquest.Service) error {
	if service == nil {
		return fmt.Errorf("world: nil commission service")
	}
	s.todayQuests = service
	return nil
}

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

// NPCController checks whether any owned pack has completed its main story,
// rather than requiring completion of the board's own pack.
func (s *Service) CommissionPackUnlocked(pack int) bool {
	if !s.packUnlocked(pack) || s.storyCatalog == nil {
		return false
	}
	for id, design := range s.storyCatalog.Packs {
		if !s.packUnlocked(id) || len(design.MainQuestIDs) == 0 {
			continue
		}
		complete := true
		for _, quest := range design.MainQuestIDs {
			if !s.state.QuestCleared(quest, id, 0) {
				complete = false
				break
			}
		}
		if complete {
			return true
		}
	}
	return false
}
