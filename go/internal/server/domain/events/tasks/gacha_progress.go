package eventtasks

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

func (s *Service) AttachGachaHistory(source func(command.Context, uint64) ([]byte, error)) {
	s.gachaHistory = source
}

// Reconstruct missing progress from durable ownership and successful draw history.
func (s *Service) reconcileGachaProgress(ctx command.Context, inventory *InventorySnapshot, history bool) error {
	type progress struct {
		schedule events.Schedule
		task     gamedata.EventTask
		start    int64
		value    uint64
	}
	var pending []progress
	needCostumes := false
	earliest := int64(-1)
	for _, v := range s.taskSchedules() {
		if v.Type != 4 || !s.active(v) {
			continue
		}
		for _, t := range s.design.Missions {
			if (t.Type != 103 && t.Type != 104 && (t.Type != 105 || !history || s.gachaHistory == nil)) || !s.availableTask(ctx, v, t) {
				continue
			}
			start := v.Start
			now := s.now().UTC()
			period := ""
			switch s.design.MissionGroups[v.ID].Type {
			case 1:
				start = max(start, now.Truncate(24*time.Hour).UnixMilli())
				period = s.day()
			case 2:
				days := (int(now.Weekday()) + 6) % 7
				start = max(start, now.Truncate(24*time.Hour).AddDate(0, 0, -days).UnixMilli())
				year, week := now.ISOWeek()
				period = fmt.Sprintf("%d-%d", year, week)
			}
			if m := s.state.Missions[scheduleKey(v)+"/"+key(t.ID)]; m != nil && m.Period == period && (m.Claimed || m.Value >= t.Target) {
				continue
			}
			pending = append(pending, progress{schedule: v, task: t, start: start})
			if t.Type == 105 {
				if earliest < 0 || start < earliest {
					earliest = start
				}
			} else {
				needCostumes = true
			}
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if needCostumes && inventory == nil && s.provider != nil {
		snapshot, err := s.inventorySnapshot(ctx)
		if err != nil {
			return err
		}
		inventory = &snapshot
	}
	for i := range pending {
		p := &pending[i]
		if p.task.Type == 105 || inventory == nil {
			continue
		}
		for _, costume := range inventory.Costumes {
			if p.task.SubType != 0 && p.task.SubType != costume.ID || len(p.task.Params) != 0 && !slices.Contains(p.task.Params, costume.ID) {
				continue
			}
			if p.task.Type == 103 {
				p.value = min(p.task.Target, p.value+1)
			} else {
				p.value = min(p.task.Target, p.value+min(costume.Level, p.task.Target))
			}
		}
	}
	if earliest >= 0 {
		raw, err := s.gachaHistory(ctx, uint64(max(0, earliest-1)))
		if err != nil {
			return err
		}
		if err = wire.Walk(raw, func(f wire.Field) error {
			if f.Number != 1 || f.Type != 2 {
				return nil
			}
			group, count, at := scalar(f.Value, 1), scalar(f.Value, 4), scalar(f.Value, 13)
			for i := range pending {
				p := &pending[i]
				if p.task.Type == 105 && (p.task.SubType == 0 || p.task.SubType == group) && (len(p.task.Params) == 0 || slices.Contains(p.task.Params, group)) && at >= uint64(p.start) && at < uint64(p.schedule.End) && at <= uint64(s.now().UnixMilli()) {
					p.value += min(count, p.task.Target-p.value)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	before, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	changed := false
	for _, p := range pending {
		if p.value == 0 {
			continue
		}
		m := s.mission(p.schedule, p.task.ID)
		if p.value > m.Value {
			m.Value = p.value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err = s.save(ctx); err != nil {
		_ = json.Unmarshal(before, &s.state)
	}
	return err
}
