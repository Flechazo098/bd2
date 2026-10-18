package eventtasks

import (
	"math"
	"slices"

	"bd2server/internal/server/domain/events"
)

func (s *Service) passSchedules() []events.Schedule {
	permanent := make([]uint64, 0)
	for id, design := range s.design.Passes {
		if design.ScheduleType == 0 && design.NewbieGroup > 0 && design.NewbieStep > 0 {
			permanent = append(permanent, id)
		}
	}
	rows := slices.DeleteFunc(s.registry.List(), func(row events.Schedule) bool {
		return row.Type == 5 && slices.Contains(permanent, row.ID)
	})
	slices.Sort(permanent)
	for _, id := range permanent {
		// Permanent guide passes have a stable local identity, not a public
		// event calendar entry. The native PassInfo includes them directly.
		rows = append(rows, events.Schedule{Type: 5, ID: id, End: math.MaxInt64})
	}
	return rows
}

func (s *Service) reconcileGuidePass(v events.Schedule) {
	design := s.design.Passes[v.ID]
	if design.ScheduleType != 0 || design.NewbieGroup == 0 || design.NewbieStep == 0 {
		return
	}
	missionSchedule, err := s.find(4, design.MissionGroup)
	if err != nil {
		return
	}
	var earned uint64
	for _, task := range s.design.Missions {
		if !slices.Contains(s.design.MissionGroups[design.MissionGroup].Groups, task.Group) {
			continue
		}
		if progress := s.state.Missions[scheduleKey(missionSchedule)+"/"+key(task.ID)]; progress != nil && progress.Claimed {
			earned += task.PassExp
		}
	}
	progress := s.pass(v)
	progress.Exp = max(progress.Exp, earned)
}
