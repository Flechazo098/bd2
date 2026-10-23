package world

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
)

// AttachMissionProgress reports field actions to mission conditions.
func (s *Service) AttachMissionProgress(record func(command.Context, conditions.Event) error) {
	s.missionProgress = record
}

func (s *Service) recordMission(ctx command.Context, event conditions.Event) error {
	if s.missionProgress == nil {
		return nil
	}
	return s.missionProgress(ctx, event)
}
