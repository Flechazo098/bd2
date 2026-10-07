package achievements

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

func (s *AchievementService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/AchievementInfo" && path != "/AchievementUpdate" {
		return 0, nil, false, nil
	}

	fail := func(err error) (int, []byte, bool, error) { return 0, nil, true, err }
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return fail(ErrInvalidRequest)
	}
	state, err := s.load(ctx)
	if err != nil {
		return fail(err)
	}
	if path == "/AchievementInfo" {
		claimed := map[gamedata.AchievementKey]bool{}
		if s.claims != nil {
			claimed = s.claims.ClaimedAchievementIDs()
		}
		for key := range claimed {
			group := strconv.Itoa(int(key.GroupID))
			if _, found := state.Counts[group]; !found && len(s.design.Groups[int(key.GroupID)]) > 0 {
				state.Counts[group] = 0
			}
		}
		groups := make([]int, 0, len(state.Counts))
		for key := range state.Counts {
			group, _ := strconv.Atoi(key)
			groups = append(groups, group)
		}
		sort.Ints(groups)
		var response []byte
		for _, group := range groups {
			for _, content := range s.design.Groups[group] {
				row := wire.AppendVarint(nil, 1, uint64(group))
				row = wire.AppendVarint(row, 2, uint64(state.Counts[strconv.Itoa(group)]))
				var maxID uint64
				// AchievementPacket.UpdateAchievementCount initializes title MaxClearId
				// to 1000; this is the client protocol default, not a granted tier.
				if content == 1 {
					maxID = 1000
				}
				for key := range claimed {
					if key.GroupID == uint64(group) && key.ContentsGroup == uint64(content) && key.ID > maxID {
						maxID = key.ID
					}
				}
				if maxID > 0 {
					row = wire.AppendVarint(row, 3, maxID)
				}
				if content != 0 {
					row = wire.AppendVarint(row, 4, uint64(content))
				}
				response = wire.AppendBytes(response, 1, row)
			}
		}
		// Only the mission domain's actual claims advance max_clear_id.
		return 166, response, true, nil
	}
	group, found, err := wire.Varint(request, 2)
	if err != nil || !found || group == 0 || group > math.MaxInt32 || len(s.design.Groups[int(group)]) == 0 {
		return fail(ErrInvalidRequest)
	}
	add, found, err := wire.Varint(request, 3)
	if err != nil || !found || add == 0 || add > math.MaxInt32 || ctx.SessionID == "" {
		return fail(ErrInvalidRequest)
	}
	receipt := achievementReceipt{Sequence: seq, Group: int(group), Add: int(add)}
	replayKey := ctx.SessionID + "/" + strconv.FormatUint(seq, 10)
	if raw, found, err := s.store.LoadEntry(ctx.State, "missions", "achievement_replays", replayKey); err != nil {
		return fail(err)
	} else if found {
		var previous achievementReceipt
		if err = json.Unmarshal(raw, &previous); err != nil {
			return fail(err)
		}
		if previous == receipt {
			return 167, nil, true, nil
		}
		return fail(fmt.Errorf("achievement: conflicting request sequence"))
	}
	if previous, ok := state.Receipts[ctx.SessionID]; ok && seq <= previous.Sequence {
		if previous == receipt {
			return 167, nil, true, nil
		}
		return fail(fmt.Errorf("achievement: stale or conflicting request sequence"))
	}
	key := strconv.Itoa(int(group))
	current := state.Counts[key]
	if current > math.MaxInt64-int64(add) {
		return fail(fmt.Errorf("achievement: counter overflow"))
	}
	state.Counts[key] = current + int64(add)
	state.Receipts[ctx.SessionID] = receipt
	raw, err := json.Marshal(state.Counts[key])
	if err != nil {
		return fail(err)
	}
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return fail(err)
	}
	changes := []stateio.EntryMutation{{Bucket: "achievement_counts", Key: key, Payload: raw}, {Bucket: "achievement_receipts", Key: ctx.SessionID, Payload: receiptRaw}, {Bucket: "achievement_replays", Key: replayKey, Payload: receiptRaw}}
	// Retain a complete retry window so a committed BatchRequest whose response
	// was lost can replay several updates, not just the final update in the batch.
	if seq > 256 {
		entries, err := s.store.ListEntries(ctx.State, "missions", "achievement_replays")
		if err != nil {
			return fail(err)
		}
		prefix := ctx.SessionID + "/"
		for replayKey := range entries {
			if suffix, found := strings.CutPrefix(replayKey, prefix); found {
				old, err := strconv.ParseUint(suffix, 10, 64)
				if err != nil {
					return fail(err)
				}
				if old <= seq-256 {
					changes = append(changes, stateio.EntryMutation{Bucket: "achievement_replays", Key: replayKey, Delete: true})
				}
			}
		}
	}
	if err = s.store.SaveWithEntries(ctx.State, "missions", nil, changes); err != nil {
		return fail(err)
	}
	return 167, nil, true, nil
}

// SyncRecordedHistory must run within the startup account transaction. Retained
// real grants establish a lower bound; inventory never substitutes for history.
func (s *GameplayAchievementObserver) SyncRecordedHistory(ctx command.Context) error {
	snapshot, err := s.provider.Snapshot(ctx)
	if err != nil {
		return err
	}
	var ids []string
	for identity := range snapshot.GachaGrants {
		ids = append(ids, identity)
	}
	sort.Strings(ids)
	var events []GameplayAchievementRecordedEvent
	for _, identity := range ids {
		count := snapshot.GachaGrants[identity]
		if count == 0 {
			continue
		}
		events = append(events, GameplayAchievementRecordedEvent{Identity: "gacha-grant:" + identity, Type: 54, Count: count})
	}
	if batch, ok := s.counter.(gameplayAchievementProgressBatch); ok {
		_, _, err := batch.ApplyGameplayProgress(ctx, snapshot.Conditions, events)
		return err
	}
	for _, condition := range snapshot.Conditions {
		if _, err := s.counter.SetCondition(ctx, condition.Type, condition.SubType, condition.Value); err != nil {
			return err
		}
	}
	for _, event := range events {
		if _, err := s.counter.RecordEvent(ctx, event.Identity, event.Type, event.SubType, event.Count); err != nil {
			return err
		}
	}
	return nil
}

func (s *GameplayAchievementObserver) BeforeDispatch(ctx command.Context, _ string, _ []byte) error {
	version := s.observationVersion()
	if s.ready && version != "" && version == s.version {
		return nil
	}
	s.ready = false
	var err error
	s.before, err = s.provider.Snapshot(ctx)
	if err != nil {
		return err
	}
	if batch, ok := s.counter.(gameplayAchievementProgressBatch); ok {
		s.counters, s.conditionValues, err = batch.ApplyGameplayProgress(ctx, s.before.Conditions, nil)
		if err == nil {
			s.version = s.observationVersion()
			s.ready = true
		}
		return err
	}
	s.counters, err = s.counter.CounterValues(ctx)
	if err != nil {
		return err
	}
	for _, condition := range s.before.Conditions {
		if _, err := s.counter.SetCondition(ctx, condition.Type, condition.SubType, condition.Value); err != nil {
			return err
		}
	}
	s.conditionValues, err = s.counter.CounterValues(ctx)
	if err != nil {
		return err
	}
	s.version = s.observationVersion()
	s.ready = true
	return nil
}

func (s *GameplayAchievementObserver) AfterDispatch(ctx command.Context, path string, request, response []byte) ([]byte, error) {
	version := s.observationVersion()
	if s.ready && version != "" && version == s.version {
		notify := achievementCountNotifications(s.counters, s.conditionValues)
		s.counters = s.conditionValues
		return notify, nil
	}
	s.ready = false
	after, err := s.provider.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	batch, batches := s.counter.(gameplayAchievementProgressBatch)
	if !batches {
		for _, condition := range after.Conditions {
			if _, err := s.counter.SetCondition(ctx, condition.Type, condition.SubType, condition.Value); err != nil {
				return nil, err
			}
		}
	}
	events, err := s.provider.Events(path, request, response, s.before, after)
	if err != nil {
		return nil, err
	}
	var recorded []GameplayAchievementRecordedEvent
	if len(events) > 0 {
		seq, found, err := wire.Varint(request, 1)
		if err != nil || !found || seq == 0 || ctx.SessionID == "" {
			return nil, fmt.Errorf("achievement: gameplay event sequence unavailable")
		}
		for i, event := range events {
			identity := fmt.Sprintf("%s/%s/%d/%d/%s", ctx.SessionID, path, seq, i, event.Identity)
			if event.StableIdentity {
				identity = event.Identity
			}
			recorded = append(recorded, GameplayAchievementRecordedEvent{Identity: identity, Type: event.Type, SubType: event.SubType, Count: event.Count})
		}
	}
	var values map[int]uint64
	if batches {
		_, values, err = batch.ApplyGameplayProgress(ctx, after.Conditions, recorded)
	} else {
		for _, event := range recorded {
			if _, err = s.counter.RecordEvent(ctx, event.Identity, event.Type, event.SubType, event.Count); err != nil {
				return nil, err
			}
		}
		values, err = s.counter.CounterValues(ctx)
	}
	if err != nil {
		return nil, err
	}
	notify := achievementCountNotifications(s.counters, values)
	s.before, s.counters, s.conditionValues = after, values, values
	s.version = s.observationVersion()
	s.ready = true
	return notify, nil
}

func achievementCountNotifications(before, values map[int]uint64) []byte {
	var groups []int
	for group, value := range values {
		if before[group] != value {
			groups = append(groups, group)
		}
	}
	sort.Ints(groups)
	var notify []byte
	for _, group := range groups {
		row := wire.AppendVarint(nil, 1, uint64(group))
		row = wire.AppendVarint(row, 2, values[group])
		row = wire.AppendVarint(row, 3, 1)
		notify = wire.AppendBytes(notify, 2, row)
	}
	return notify
}

// RecordEvent is an authoritative event increment, independently replayable
// by a domain operation identity. It does not infer cumulative counts from
// inventory balances, which lose consumed and discarded items.
func (s *AchievementService) RecordEvent(ctx command.Context, identity string, conditionType, subType, count uint64) ([][]byte, error) {
	if identity == "" || count == 0 || count > math.MaxInt64 {
		return nil, fmt.Errorf("achievement: invalid event")
	}

	digest := fmt.Sprintf("%d/%d/%d", conditionType, subType, count)
	if raw, found, err := s.store.LoadEntry(ctx.State, "missions", "achievement_events", identity); err != nil {
		return nil, err
	} else if found {
		if string(raw) != digest {
			return nil, fmt.Errorf("achievement: event replay conflicts")
		}
		return nil, nil
	}
	state, err := s.load(ctx)
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
	if err := s.store.SaveWithEntries(ctx.State, "missions", nil, changes); err != nil {
		return nil, err
	}
	return updates, nil
}

func (s *AchievementService) SetCondition(ctx command.Context, conditionType, subType, value uint64) ([][]byte, error) {
	if value > math.MaxInt64 {
		return nil, fmt.Errorf("achievement: invalid absolute value")
	}

	state, err := s.load(ctx)
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
	if err := s.store.SaveWithEntries(ctx.State, "missions", nil, changes); err != nil {
		return nil, err
	}
	return updates, nil
}
