package world

import "bd2server/internal/server/domain/command"

func (s *Service) storyPackUnlocked(ctx command.Context, id int) bool {
	pack, exists := s.storyCatalog.Packs[id]
	if !exists {
		return false
	}
	// A server-selected entry chapter is explicitly available to this account.
	// This does not invent tickets for the remaining chapter catalog.
	if id == s.startingPack() {
		return true
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
		for _, item := range s.inventory.All(ctx) {
			if item.Type == 19 && item.ID == pack.Open.TicketID && item.Count > 0 {
				return true
			}
		}
		return false
	}
	return true
}
