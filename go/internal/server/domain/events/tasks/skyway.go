package eventtasks

import (
	"bd2server/internal/server/domain/command"
	"slices"
)

func (s *Service) RecordSkyWayClear(ctx command.Context, positionGroup, difficulty uint64) error {
	changed := false
	for _, schedule := range s.taskSchedules() {
		if schedule.Type != 4 || !s.active(schedule) {
			continue
		}
		for _, task := range s.design.Missions {
			if task.Type != 24 || task.SubType != positionGroup || !slices.Contains(task.Params, difficulty) || !s.availableTask(ctx, schedule, task) {
				continue
			}
			previous := s.state.Missions[scheduleKey(schedule)+"/"+key(task.ID)]
			var prior mission
			if previous != nil {
				prior = *previous
			}
			current := s.mission(schedule, task.ID)
			if previous == nil || prior != *current {
				changed = true
			}
			if !current.Claimed && current.Value < task.Target {
				current.Value++
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return s.save(ctx)
}
