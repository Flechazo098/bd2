package eventtasks

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/progression/conditions"
)

// AccountState reports, for each condition, the progress durable account
// state already proves (character levels, cleared quests, castle floors, ...).
// Conditions that state cannot prove report zero.
type AccountState func(ctx command.Context, specs []conditions.Spec) ([]uint64, error)

// AttachAccountState supplies the state-derived progress source. Such tasks
// complete from state rather than from live events only, so progress made
// before a task's schedule existed, or while it was locked, still counts.
func (s *Service) AttachAccountState(source AccountState) { s.accountState = source }

// ReconcileAccountState backfills every active state-derived task and persists
// only when progress changed. Values never decrease and never exceed targets.
func (s *Service) ReconcileAccountState(ctx command.Context) error {
	changed, err := s.reconcileAccountState(ctx)
	if err != nil || !changed {
		return err
	}
	return s.save(ctx)
}

func (s *Service) reconcileAccountState(ctx command.Context) (bool, error) {
	if s.accountState == nil {
		return false, nil
	}
	type pending struct {
		schedule events.Schedule
		task     gamedata.EventTask
	}
	var tasks []pending
	var specs []conditions.Spec
	for _, schedule := range s.taskSchedules() {
		if schedule.Type != 4 || !s.active(schedule) {
			continue
		}
		period := s.missionPeriod(schedule)
		for _, task := range s.design.Missions {
			if !s.availableTask(ctx, schedule, task) {
				continue
			}
			if m := s.state.Missions[scheduleKey(schedule)+"/"+key(task.ID)]; m != nil && m.Period == period && (m.Claimed || m.Value >= missionTarget(task)) {
				continue
			}
			tasks = append(tasks, pending{schedule, task})
			specs = append(specs, taskSpec(task))
		}
	}
	if len(tasks) == 0 {
		return false, nil
	}
	values, err := s.accountState(ctx, specs)
	if err != nil {
		return false, err
	}
	changed := false
	for i, p := range tasks {
		if i >= len(values) || values[i] == 0 {
			continue
		}
		m := s.mission(p.schedule, p.task.ID)
		if value := min(values[i], missionTarget(p.task)); value > m.Value {
			m.Value = value
			changed = true
		}
	}
	return changed, nil
}
