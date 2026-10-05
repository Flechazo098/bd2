package missions

import (
	"errors"
	"sort"

	"bd2server/internal/server/gamedata"
)

// These values are Define_MissionConditionType protocol enums, not mission IDs.
const (
	ConditionConnect     uint64 = 0
	ConditionMonsterKill uint64 = 2
	ConditionGachaBuy    uint64 = 9
)

// RecordEvent applies an authoritative gameplay count to every matching regular
// mission. The caller supplies quest unlock state; locked and scheduled event
// missions cannot become eligible merely because they share the condition.
func (s *Service) RecordEvent(conditionType, subType, count uint64, unlocked func(uint64, uint64) bool) error {
	if observer, ok := s.eventHandler.(interface {
		RecordEvent(uint64, uint64, uint64, func(uint64, uint64) bool) error
	}); ok {
		if err := observer.RecordEvent(conditionType, subType, count, unlocked); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rolloverLocked(); err != nil {
		return err
	}
	return s.recordEventLocked(conditionType, subType, count, unlocked)
}

// RecordLogin records at most one connection per daily period. The existing
// progress map stores the period marker, so reconnects and restarts cannot
// inflate weekly connection missions and no additional save format is needed.
func (s *Service) RecordLogin(unlocked func(uint64, uint64) bool) error {
	if observer, ok := s.eventHandler.(interface {
		RecordLogin(func(uint64, uint64) bool) error
	}); ok {
		if err := observer.RecordLogin(unlocked); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rolloverLocked(); err != nil {
		return err
	}
	if s.state.Progress["0/event/connect"] != 0 {
		return nil
	}
	return s.recordEventLocked(ConditionConnect, 0, 1, unlocked)
}

// CompleteSingleTargetEvent covers evidence of at least one occurrence, without
// inventing a count or subtype for richer missions (e.g. a monster encounter win).
func (s *Service) CompleteSingleTargetEvent(conditionType uint64, unlocked func(uint64, uint64) bool) error {
	if observer, ok := s.eventHandler.(interface {
		CompleteSingleTargetEvent(uint64, func(uint64, uint64) bool) error
	}); ok {
		if err := observer.CompleteSingleTargetEvent(conditionType, unlocked); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rolloverLocked(); err != nil {
		return err
	}
	next := cloneSnapshot(s.state)
	for key, c := range s.design.Conditions {
		if key.GroupType > 1 || c.Type != conditionType || c.SubType != 0 || c.TargetValue != 1 || len(c.Params) != 0 {
			continue
		}
		if (c.UnlockPack != 0 || c.UnlockQuest != 0) && (unlocked == nil || !unlocked(c.UnlockPack, c.UnlockQuest)) {
			continue
		}
		name := missionName(key)
		if next.Progress[name] >= 1 {
			continue
		}
		next.Progress[name] = 1
		s.applyCompletionDependencies(&next, key)
	}
	return s.commit(next)
}

func (s *Service) recordEventLocked(conditionType, subType, count uint64, unlocked func(uint64, uint64) bool) error {
	if count == 0 {
		return errors.New("missions: event count is zero")
	}
	next := cloneSnapshot(s.state)
	var keys []gamedata.MissionKey
	for key, condition := range s.design.Conditions {
		if key.GroupType > 1 || condition.Type != conditionType || len(condition.Params) != 0 {
			continue
		}
		if condition.SubType != 0 && ((condition.SubTypeComparison == 0 && condition.SubType != subType) || (condition.SubTypeComparison == 1 && subType < condition.SubType)) {
			continue
		}
		if (condition.UnlockPack != 0 || condition.UnlockQuest != 0) && (unlocked == nil || !unlocked(condition.UnlockPack, condition.UnlockQuest)) {
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
		target := s.design.Conditions[key].TargetValue
		if target == 0 {
			target = 1
		}
		before := next.Progress[name]
		if before >= target {
			continue
		}
		increment := count
		if increment > target-before {
			increment = target - before
		}
		next.Progress[name] = before + increment
		if next.Progress[name] == target {
			s.applyCompletionDependencies(&next, key)
		}
	}
	if conditionType == ConditionConnect {
		next.Progress["0/event/connect"] = 1
	}
	return s.commit(next)
}
