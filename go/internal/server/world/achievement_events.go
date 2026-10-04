package world

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"

	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func (s *AchievementService) AchievementValue(groupID uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return 0, err
	}
	return uint64(state.Counts[strconv.FormatUint(groupID, 10)]), nil
}

// RecordEvent is an authoritative event increment, independently replayable
// by a domain operation identity. It does not infer cumulative counts from
// inventory balances, which lose consumed and discarded items.
func (s *AchievementService) RecordEvent(identity string, conditionType, subType, count uint64) ([][]byte, error) {
	if identity == "" || count == 0 || count > math.MaxInt64 {
		return nil, fmt.Errorf("achievement: invalid event")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	digest := fmt.Sprintf("%d/%d/%d", conditionType, subType, count)
	if raw, found, err := s.store.LoadEntry("missions", "achievement_events", identity); err != nil {
		return nil, err
	} else if found {
		if string(raw) != digest {
			return nil, fmt.Errorf("achievement: event replay conflicts")
		}
		return nil, nil
	}
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	var groups []int
	for group, c := range s.design.Conditions {
		if c.Type == conditionType && c.SubType == subType {
			groups = append(groups, group)
		}
	}
	sort.Ints(groups)
	changes := []stateio.EntryMutation{{Bucket: "achievement_events", Key: identity, Payload: []byte(digest)}}
	var updates [][]byte
	for _, group := range groups {
		key := strconv.Itoa(group)
		if state.Counts[key] > math.MaxInt64-int64(count) {
			return nil, fmt.Errorf("achievement: event overflow")
		}
		value := state.Counts[key] + int64(count)
		raw, _ := json.Marshal(value)
		changes = append(changes, stateio.EntryMutation{Bucket: "achievement_counts", Key: key, Payload: raw})
		update := wire.AppendVarint(nil, 1, uint64(group))
		update = wire.AppendVarint(update, 2, uint64(value))
		update = wire.AppendVarint(update, 3, 1)
		updates = append(updates, update)
	}
	if err := s.store.SaveWithEntries("missions", nil, changes); err != nil {
		return nil, err
	}
	return updates, nil
}

// SetCondition only accepts exact current-state conditions; cumulative events use RecordEvent.
func (s *AchievementService) SetCondition(conditionType, subType, value uint64) ([][]byte, error) {
	if value > math.MaxInt64 {
		return nil, fmt.Errorf("achievement: invalid absolute value")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	var groups []int
	for group, c := range s.design.Conditions {
		if c.Type == conditionType && c.SubType == subType {
			groups = append(groups, group)
		}
	}
	sort.Ints(groups)
	var changes []stateio.EntryMutation
	var updates [][]byte
	for _, group := range groups {
		key := strconv.Itoa(group)
		if state.Counts[key] == int64(value) {
			continue
		}
		raw, _ := json.Marshal(value)
		changes = append(changes, stateio.EntryMutation{Bucket: "achievement_counts", Key: key, Payload: raw})
		update := wire.AppendVarint(nil, 1, uint64(group))
		update = wire.AppendVarint(update, 2, value)
		updates = append(updates, wire.AppendVarint(update, 3, 1))
	}
	if len(changes) == 0 {
		return nil, nil
	}
	if err := s.store.SaveWithEntries("missions", nil, changes); err != nil {
		return nil, err
	}
	return updates, nil
}

func (s *AchievementService) CounterValues() (map[int]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	out := map[int]uint64{}
	for key, value := range state.Counts {
		group, err := strconv.Atoi(key)
		if err != nil {
			return nil, err
		}
		out[group] = uint64(value)
	}
	return out, nil
}
