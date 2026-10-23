package missions

import (
	"errors"
	"sort"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
)

// RecordEvent applies an authoritative gameplay occurrence to every matching
// regular mission. The caller supplies quest unlock state; locked and scheduled
// event missions cannot become eligible merely because they share the condition.
func (s *Service) RecordEvent(ctx command.Context, event conditions.Event, unlocked func(command.Context, uint64, uint64) bool) error {
	if observer, ok := s.eventHandler.(interface {
		RecordEvent(command.Context, conditions.Event, func(command.Context, uint64, uint64) bool) error
	}); ok {
		if err := observer.RecordEvent(ctx, event, unlocked); err != nil {
			return err
		}
	}

	if err := s.rolloverLocked(ctx); err != nil {
		return err
	}
	return s.recordEventLocked(ctx, event, unlocked)
}

// RecordLogin records at most one connection per daily period. The existing
// progress map stores the period marker, so reconnects and restarts cannot
// inflate weekly connection missions and no additional save format is needed.
func (s *Service) RecordLogin(ctx command.Context, unlocked func(command.Context, uint64, uint64) bool) error {
	if observer, ok := s.eventHandler.(interface {
		RecordLogin(ctx command.Context, _ func(command.Context, uint64, uint64) bool) error
	}); ok {
		if err := observer.RecordLogin(ctx, unlocked); err != nil {
			return err
		}
	}

	if err := s.rolloverLocked(ctx); err != nil {
		return err
	}
	if s.state.Progress["0/event/connect"] != 0 {
		return nil
	}
	return s.recordEventLocked(ctx, conditions.Event{Type: conditions.Connect, Count: 1}, unlocked)
}

func conditionSpec(c gamedata.MissionCondition) conditions.Spec {
	return conditions.Spec{Type: c.Type, SubType: c.SubType, SubTypeMoreCheck: c.SubTypeComparison, Params: c.Params}
}

func (s *Service) recordEventLocked(ctx command.Context, event conditions.Event, unlocked func(command.Context, uint64, uint64) bool) error {
	if event.Count == 0 {
		return errors.New("missions: event count is zero")
	}
	next := cloneSnapshot(s.state)
	var keys []gamedata.MissionKey
	for key, condition := range s.design.Conditions {
		if key.GroupType > 1 || !s.missionActive(key) || !conditionSpec(condition).Matches(event) {
			continue
		}
		if (condition.UnlockPack != 0 || condition.UnlockQuest != 0) && (unlocked == nil || !unlocked(ctx, condition.UnlockPack, condition.UnlockQuest)) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.GroupType != b.GroupType {
			return a.GroupType < b.GroupType
		}
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		return a.ID < b.ID
	})
	for _, key := range keys {
		name := missionName(key)
		target := conditions.Target(s.design.Conditions[key].TargetValue)
		before := next.Progress[name]
		if before >= target {
			continue
		}
		increment := min(event.Count, target-before)
		next.Progress[name] = before + increment
		if next.Progress[name] == target {
			s.applyCompletionDependencies(&next, key)
		}
	}
	if event.Type == conditions.Connect {
		next.Progress["0/event/connect"] = 1
	}
	return s.commit(ctx, next)
}
