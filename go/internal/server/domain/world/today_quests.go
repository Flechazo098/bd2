package world

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/todayquest"
	"fmt"
)

func (s *Service) AttachTodayQuests(service *todayquest.Service) error {
	if service == nil {
		return fmt.Errorf("world: nil commission service")
	}
	s.todayQuests = service
	return nil
}

// NPCController checks whether any owned pack has completed its main story,
// rather than requiring completion of the board's own pack.
func (s *Service) CommissionPackUnlocked(ctx command.Context, pack int) bool {
	if !s.packUnlocked(ctx, pack) || s.storyCatalog == nil {
		return false
	}
	for id, design := range s.storyCatalog.Packs {
		if !s.packUnlocked(ctx, id) || len(design.MainQuestIDs) == 0 {
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
