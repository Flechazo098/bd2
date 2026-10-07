package achievements

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

type GameplayAchievementRecordedEvent struct {
	Identity             string
	Type, SubType, Count uint64
}

// ApplyGameplayProgress keeps one observer boundary on one validated counter
// snapshot. Conditions are applied first and events retain their original
// order. Event receipts and all counter changes are written atomically; the
// next boundary loads fresh state from its transaction, including after retry.
func (s *AchievementService) ApplyGameplayProgress(ctx command.Context, conditions []GameplayAchievementCondition, events []GameplayAchievementRecordedEvent) (map[int]uint64, map[int]uint64, error) {

	state, err := s.load(ctx)
	if err != nil {
		return nil, nil, err
	}
	values := func() map[int]uint64 {
		out := map[int]uint64{}
		for key, value := range state.Counts {
			group, _ := strconv.Atoi(key) // load validated every key.
			out[group] = uint64(value)
		}
		return out
	}
	before := values()
	groups := map[[2]uint64][]int{}
	for group, condition := range s.design.Conditions {
		key := [2]uint64{condition.Type, condition.SubType}
		groups[key] = append(groups[key], group)
	}
	for key := range groups {
		sort.Ints(groups[key])
	}
	var changes []stateio.EntryMutation
	set := func(group int, value int64) {
		key := strconv.Itoa(group)
		state.Counts[key] = value
		raw, _ := json.Marshal(value)
		changes = append(changes, stateio.EntryMutation{Bucket: "achievement_counts", Key: key, Payload: raw})
	}
	for _, condition := range conditions {
		if condition.Value > math.MaxInt64 {
			return nil, nil, fmt.Errorf("achievement: invalid absolute value")
		}
		for _, group := range groups[[2]uint64{condition.Type, condition.SubType}] {
			if state.Counts[strconv.Itoa(group)] != int64(condition.Value) {
				set(group, int64(condition.Value))
			}
		}
	}
	pending := map[string]string{}
	for _, event := range events {
		if event.Identity == "" || event.Count == 0 || event.Count > math.MaxInt64 {
			return nil, nil, fmt.Errorf("achievement: invalid event")
		}
		digest := fmt.Sprintf("%d/%d/%d", event.Type, event.SubType, event.Count)
		raw, found, err := s.store.LoadEntry(ctx.State, "missions", "achievement_events", event.Identity)
		if err != nil {
			return nil, nil, err
		}
		if prior, exists := pending[event.Identity]; exists {
			raw, found = []byte(prior), true
		}
		if found {
			if string(raw) != digest {
				return nil, nil, fmt.Errorf("achievement: event replay conflicts")
			}
			continue
		}
		for _, group := range groups[[2]uint64{event.Type, event.SubType}] {
			old := state.Counts[strconv.Itoa(group)]
			if old > math.MaxInt64-int64(event.Count) {
				return nil, nil, fmt.Errorf("achievement: event overflow")
			}
			set(group, old+int64(event.Count))
		}
		pending[event.Identity] = digest
		changes = append(changes, stateio.EntryMutation{Bucket: "achievement_events", Key: event.Identity, Payload: []byte(digest)})
	}
	if len(changes) != 0 {
		if err := s.store.SaveWithEntries(ctx.State, "missions", nil, changes); err != nil {
			return nil, nil, err
		}
	}
	return before, values(), nil
}

func (s *AchievementService) AchievementValue(ctx command.Context, groupID uint64) (uint64, error) {

	state, err := s.load(ctx)
	if err != nil {
		return 0, err
	}
	return uint64(state.Counts[strconv.FormatUint(groupID, 10)]), nil
}

// SetCondition only accepts exact current-state conditions; cumulative events use RecordEvent.

func (s *AchievementService) CounterValues(ctx command.Context) (map[int]uint64, error) {

	state, err := s.load(ctx)
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
