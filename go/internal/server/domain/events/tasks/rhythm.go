package eventtasks

import (
	"slices"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
)

func (s *Service) RecordRhythmResult(ctx command.Context, result events.RhythmResult) error {
	changed := false
	for _, schedule := range s.taskSchedules() {
		if schedule.Type != 4 || !s.active(schedule) {
			continue
		}
		for _, task := range s.design.Missions {
			if !s.availableTask(ctx, schedule, task) || !rhythmTaskSatisfied(task, result) {
				continue
			}
			progress := s.mission(schedule, task.ID)
			if progress.Claimed || progress.Value >= task.Target {
				continue
			}
			progress.Value++
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.save(ctx)
}

func (s *Service) ReconcileRhythmResults(ctx command.Context, results []events.RhythmResult) error {
	changed := false
	for _, schedule := range s.taskSchedules() {
		if schedule.Type != 4 || !s.active(schedule) {
			continue
		}
		group := s.design.MissionGroups[schedule.ID]
		if group.Type != 0 || group.ScheduleType != 0 {
			continue
		}
		for _, task := range s.design.Missions {
			if task.Type < 49 || task.Type > 53 || !s.availableTask(ctx, schedule, task) {
				continue
			}
			var value uint64
			for _, result := range results {
				if rhythmTaskSatisfied(task, result) {
					value++
				}
			}
			value = min(value, task.Target)
			if value == 0 {
				continue
			}
			progress := s.mission(schedule, task.ID)
			if !progress.Claimed && value > progress.Value {
				progress.Value = value
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return s.save(ctx)
}

func rhythmTaskSatisfied(task gamedata.EventTask, result events.RhythmResult) bool {
	switch task.Type {
	case 49:
		return len(task.Params) == 1 && result.Judgments[task.SubType] >= task.Params[0]
	case 50:
		return task.SubType == result.Music && result.Combo > 0
	case 51:
		return result.Score >= task.SubType
	case 52:
		return task.SubType == result.Music && len(task.Params) == 1 && result.Grade > 0 && result.Grade <= task.Params[0]
	case 53:
		return task.SubType == result.Music && slices.Contains(task.Params, result.Difficulty)
	}
	return false
}
