package eventtasks

import "bd2server/internal/server/domain/command"

import (
	"fmt"
	"strings"
)

// The baseline belongs to the current account transaction, not a client session.
// Reads and retries therefore do not replay historical progress as notifications.
type missionNoticeKey struct {
	Schedule           string
	Event, Group, Task uint64
}

// visibleMissionValues is a read-only view. In particular, it must not call
// mission(), which initializes and resets persisted progress as a side effect.
func (s *Service) visibleMissionValues(ctx command.Context) map[missionNoticeKey]uint64 {
	schedules := s.taskSchedules()
	version := s.inventoryObservationVersion()
	if version != "" {
		var signature strings.Builder
		now := s.now().UTC()
		year, week := now.ISOWeek()
		fmt.Fprintf(&signature, "%s/%s/%d:%d", version, now.Format("2006-01-02"), year, week)
		for _, v := range schedules {
			fmt.Fprintf(&signature, "/%s:%d:%d:%d:%t:%d", scheduleKey(v), v.ID, v.Start, v.End, s.active(v), (now.UnixMilli()-v.Start)/86400000)
		}
		version = signature.String()
		if s.visibleMissions != nil && s.visibleVersion == version {
			return s.visibleMissions
		}
	}
	out := map[missionNoticeKey]uint64{}
	for _, v := range schedules {
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
			if !s.availableTask(ctx, v, t) {
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
	if version != "" {
		s.visibleMissions, s.visibleVersion = out, version
	}
	return out
}
