package world

import (
	"bd2server/internal/server/wire"
	"fmt"
	"sort"
)

// orderMainQuests follows the actual QuestTable links. Subquests never become
// a predecessor merely because their numeric ID lies between two main quests.
func (s *Service) orderMainQuests() error {
	for id, pack := range s.storyCatalog.Packs {
		start := 0
		for _, qid := range pack.MainQuestIDs {
			if pack.Quests[qid].PriorQuestID == 0 {
				if start != 0 {
					return fmt.Errorf("world: pack %d has multiple main quest roots", id)
				}
				start = qid
			}
		}
		var ordered []int
		seen := map[int]bool{}
		for qid := start; qid != 0; {
			q, exists := pack.Quests[qid]
			if !exists || q.Type != 0 || seen[qid] {
				return fmt.Errorf("world: invalid main quest chain pack %d quest %d", id, qid)
			}
			seen[qid] = true
			ordered = append(ordered, qid)
			qid = q.NextQuestID
		}
		if len(ordered) != len(pack.MainQuestIDs) {
			return fmt.Errorf("world: disconnected main quest chain pack %d", id)
		}
		pack.MainQuestIDs = ordered
		s.storyCatalog.Packs[id] = pack
	}
	return nil
}

func (s *Service) storyPackUnlocked(id int) bool {
	pack, exists := s.storyCatalog.Packs[id]
	if !exists {
		return false
	}
	// ContentOpen.TutorialID triggers a tutorial; the client does not use it
	// as an authorization requirement. Story NextPackID is navigation only.
	if pack.Open == nil {
		return true
	}
	if pack.Open.SquadLevel != 0 {
		if s.squadLevel == nil {
			return false
		}
		level, err := s.squadLevel()
		if err != nil || level < pack.Open.SquadLevel {
			return false
		}
	}
	if pack.Open.TicketID != 0 {
		if s.inventory == nil {
			return false
		}
		for _, item := range s.inventory.All() {
			if item.Type == 19 && item.ID == pack.Open.TicketID && item.Count > 0 {
				return true
			}
		}
		return false
	}
	return true
}

func (s *Service) storyPackDBInfoRows() [][]byte {
	ids := make([]int, 0, len(s.storyCatalog.Packs))
	for id := range s.storyCatalog.Packs {
		if s.storyPackUnlocked(id) {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	var rows [][]byte
	for _, id := range ids {
		row := wire.AppendVarint(nil, 1, uint64(id))
		mainCleared := 0
		for _, qid := range s.storyCatalog.Packs[id].MainQuestIDs {
			if s.state.QuestCleared(qid, id) {
				mainCleared++
			}
		}
		if mainCleared > 0 {
			row = wire.AppendVarint(row, 2, uint64(mainCleared))
		}
		if s.packCompleteFor(id) {
			row = wire.AppendVarint(row, 3, 1)
		}
		purchased := false
		if s.collection != nil {
			_, owned := s.collection.Grant(packPurchaseIdentity(id))
			purchased = purchased || owned
		}
		if purchased {
			row = wire.AppendVarint(row, 8, 1)
		}
		rows = append(rows, row)
	}
	if saved, found := s.state.Position(); found {
		if pack, arena := s.fieldPacks[saved.PackID]; arena && pack.MapIDs[saved.Position.MapID] {
			rows = append(rows, wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(saved.PackID)), 8, 1))
		}
	}
	return rows
}
