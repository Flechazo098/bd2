package eventtasks

import (
	"bd2server/internal/server/wire"
	"fmt"
	"sort"
)

// The baseline belongs to the current account transaction, not a client session.
// Reads and retries therefore do not replay historical progress as notifications.
type missionNoticeKey struct {
	Schedule           string
	Event, Group, Task uint64
}

// visibleMissionValues is a read-only view. In particular, it must not call
// mission(), which initializes and resets persisted progress as a side effect.
func (s *Service) visibleMissionValues() map[missionNoticeKey]uint64 {
	out := map[missionNoticeKey]uint64{}
	for _, v := range s.taskSchedules() {
		if v.Type != 4 || !s.active(v) {
			continue
		}
		group := s.design.MissionGroups[v.ID]
		period := ""
		if group.Type == 1 {
			period = s.day()
		}
		if group.Type == 2 {
			y, w := s.now().UTC().ISOWeek()
			period = fmt.Sprintf("%d-%d", y, w)
		}
		for _, t := range s.design.Missions {
			if !s.availableTask(v, t) {
				continue
			}
			k := missionNoticeKey{scheduleKey(v), v.ID, t.Group, t.ID}
			value := uint64(0)
			if m := s.state.Missions[k.Schedule+"/"+key(t.ID)]; m != nil && m.Period == period {
				value = m.Value
			}
			out[k] = value
		}
	}
	return out
}

func (s *Service) notifyMissionChanges() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.beforeMissions == nil {
		return nil, nil
	}
	current := s.visibleMissionValues()
	var keys []missionNoticeKey
	for k, value := range current {
		if value != s.beforeMissions[k] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Event != b.Event {
			return a.Event < b.Event
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		return a.Schedule < b.Schedule
	})
	var out, groupMap, rows []byte
	var eventID, groupID uint64
	flushGroup := func() {
		if len(rows) == 0 {
			return
		}
		entry := wire.AppendVarint(nil, 1, groupID)
		entry = wire.AppendBytes(entry, 2, rows)
		groupMap = wire.AppendBytes(groupMap, 1, entry)
		rows = nil
	}
	flushEvent := func() {
		flushGroup()
		if len(groupMap) == 0 {
			return
		}
		entry := wire.AppendVarint(nil, 1, eventID)
		entry = wire.AppendBytes(entry, 2, groupMap)
		out = wire.AppendBytes(out, 4, entry)
		groupMap = nil
	}
	for _, k := range keys {
		if k.Event != eventID {
			flushEvent()
			eventID = k.Event
			groupID = k.Group
		}
		if k.Group != groupID {
			flushGroup()
			groupID = k.Group
		}
		entry := wire.AppendVarint(nil, 1, k.Task)
		entry = wire.AppendVarint(entry, 2, current[k])
		rows = wire.AppendBytes(rows, 1, entry)
	}
	flushEvent()
	// Consume only this request's baseline; the next BeforeDispatch takes a fresh
	// snapshot even when a transaction rolls back or the session changes.
	s.beforeMissions = nil
	return out, nil
}
