package world

import (
	"fmt"

	"bd2server/internal/server/progress"
)

func (s *Service) startingPack() int {
	if s.startingPackID != 0 {
		return s.startingPackID
	}
	if id := s.state.StartPackID(); id != 0 {
		return id
	}
	return s.seed.PackID
}

// ConfigureStartPack keeps server policy separate from the versioned tutorial
// seed. The entry chapter is committed once when an account is initialized.
func (s *Service) ConfigureStartPack(packID int, initialize bool) error {
	if initialize {
		pack, ok := s.storyCatalog.Packs[packID]
		if !ok || len(pack.MainQuestIDs) == 0 {
			return fmt.Errorf("world: starting chapter %d has no main quest", packID)
		}
		if err := s.state.SetStartPack(packID); err != nil {
			return err
		}
		if err := s.state.SetActivePackID(packID); err != nil {
			return err
		}
		if err := s.state.SelectQuest(packID, progress.QuestSelection{QuestID: pack.MainQuestIDs[0]}); err != nil {
			return err
		}
	}
	entry := s.state.StartPackID()
	if entry != packID {
		return fmt.Errorf("world: account starting chapter %d differs from locked server chapter %d", entry, packID)
	}
	if _, ok := s.storyCatalog.Packs[entry]; !ok {
		return fmt.Errorf("world: invalid persisted starting chapter %d", entry)
	}
	s.startingPackID = entry
	active := s.state.ActivePackID()
	if active == 0 {
		return fmt.Errorf("world: account has no active chapter")
	}
	s.setCurrentPack(active)
	return nil
}

func (s *Service) tutorialRosterRestricted() bool {
	return s.startingPack() == s.seed.PackID && !s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID)
}
