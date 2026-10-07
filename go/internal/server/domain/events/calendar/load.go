package calendar

import (
	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/commerce/gacha"
	"bd2server/internal/server/domain/events"
	"fmt"
	"math"
	"time"
)

func ParseTimestamp(raw string) (uint64, error) {
	t, e := time.Parse(time.RFC3339Nano, raw)
	if e != nil {
		return 0, fmt.Errorf("invalid RFC3339 timestamp %q", raw)
	}
	ms := t.UnixMilli()
	if ms <= 0 || t.Nanosecond()%1000000 != 0 {
		return 0, fmt.Errorf("timestamp must be positive with millisecond precision: %q", raw)
	}
	return uint64(ms), nil
}
func window(start, end string, equal bool) (uint64, uint64, error) {
	a, e := ParseTimestamp(start)
	if e != nil {
		return 0, 0, e
	}
	b, e := ParseTimestamp(end)
	if e != nil {
		return 0, 0, e
	}
	if b < a || (!equal && b == a) {
		return 0, 0, fmt.Errorf("reversed/empty calendar window")
	}
	return a, b, nil
}
func int32s(values ...uint64) error {
	for _, v := range values {
		if v > math.MaxInt32 {
			return fmt.Errorf("identity/count %d exceeds protocol int32", v)
		}
	}
	return nil
}
func convertSeason(s Season) (schedule.Season, error) {
	a, b, e := window(s.Start, s.End, true)
	if e != nil {
		return schedule.Season{}, e
	}
	if s.ID == 0 {
		return schedule.Season{}, fmt.Errorf("missing season identity")
	}
	if e = int32s(s.ID, s.RankRewardGroupID); e != nil {
		return schedule.Season{}, e
	}
	return schedule.Season{ID: s.ID, StartMilliseconds: a, EndMilliseconds: b, RankRewardGroupID: s.RankRewardGroupID, Error: s.Error, Return: s.Return}, nil
}

func merge(s *Set, m Manifest, claim func(string) error) error {
	for _, v := range m.EventHubs {
		if e := claim(fmt.Sprintf("hub:%d", v.UID)); e != nil {
			return e
		}
		if v.HubID == 0 {
			return fmt.Errorf("missing hub identity")
		}
		if e := int32s(v.UID, v.HubID); e != nil {
			return e
		}
		a, b, e := window(v.Start, v.End, false)
		if e != nil {
			return e
		}
		play, e := ParseTimestamp(v.PlayEnd)
		if e != nil {
			return e
		}
		if play < a || play > b {
			return fmt.Errorf("invalid hub play end")
		}
		slots := map[uint64]bool{}
		for _, setting := range v.Settings {
			if slots[setting.Slot] {
				return fmt.Errorf("duplicate hub slot")
			}
			slots[setting.Slot] = true
			if e := int32s(setting.Slot, setting.ProgressType); e != nil {
				return e
			}
			if e := int32s(setting.EventUIDs...); e != nil {
				return e
			}
			ids := map[uint64]bool{}
			for _, uid := range setting.EventUIDs {
				if ids[uid] {
					return fmt.Errorf("duplicate hub event uid")
				}
				ids[uid] = true
			}
		}
		s.EventHubs = append(s.EventHubs, v)
	}
	for _, v := range m.MiniGameHubs {
		if e := claim(fmt.Sprintf("mini_game:%d", v.Slot)); e != nil {
			return e
		}
		if e := int32s(v.Slot, v.EventUID, v.ProgressType); e != nil {
			return e
		}
		s.MiniGameHubs = append(s.MiniGameHubs, v)
	}
	for _, v := range m.Events {
		if e := claim(eventIdentity(v)); e != nil {
			return e
		}
		if e := int32s(v.Type, v.ID, v.SubID); e != nil {
			return e
		}
		a, b, e := eventWindow(v.Start, v.End)
		if e != nil {
			return e
		}
		s.Events = append(s.Events, events.Schedule{UID: v.UID, Type: v.Type, ID: v.ID, SubID: v.SubID, Start: a, End: b})
	}
	r := events.NewRegistry()
	if e := r.Replace(s.Events); e != nil {
		return e
	}
	for n, rows := range [][]Gacha{m.Gacha, m.StepUps} {
		for _, v := range rows {
			if e := int32s(v.GroupID); e != nil {
				return e
			}
			a, b, e := window(v.Start, v.End, false)
			if e != nil {
				return e
			}
			w := gacha.ScheduleWindow{GroupID: v.GroupID, StartTime: a, EndTime: b, FreeCountBonus: v.FreeCountBonus, CashCountBonus: v.CashCountBonus}
			if n == 0 {
				s.GachaSeed.Schedules = append(s.GachaSeed.Schedules, w)
			} else {
				s.GachaSeed.StepUps = append(s.GachaSeed.StepUps, w)
			}
		}
	}
	if m.Regular != nil {
		if e := claim("regular"); e != nil {
			return e
		}
		r := m.Regular
		s.RegularService = &schedule.Service{Version: m.GameVersion, CalculateMilliseconds: r.CalculateMilliseconds, Regular: r.Regular}
		for _, v := range r.Contents {
			a, e := convertSeason(v.Current)
			if e != nil {
				return e
			}
			b, e := convertSeason(v.Next)
			if e != nil {
				return e
			}
			s.RegularService.Contents = append(s.RegularService.Contents, schedule.Content{ID: v.ID, Current: a, Next: b})
		}
	}
	if m.MonsterHunt != nil {
		if e := claim("monster_hunt"); e != nil {
			return e
		}
		h := m.MonsterHunt
		if e := int32s(h.StartRegularSeason); e != nil {
			return e
		}
		for _, v := range h.Seasons {
			if e := claim(fmt.Sprintf("hunt:%d", v.Season.ID)); e != nil {
				return e
			}
			if v.HuntID == 0 {
				return fmt.Errorf("missing hunt identity")
			}
			if e := int32s(v.HuntID, v.InfoOpenDay, v.RankRewardGroupID); e != nil {
				return e
			}
			if _, e := convertSeason(v.Season); e != nil {
				return e
			}
			end, e := ParseTimestamp(v.CalculateEndAt)
			if e != nil {
				return e
			}
			seasonEnd, _ := ParseTimestamp(v.Season.End)
			if end < seasonEnd {
				return fmt.Errorf("hunt calculation ends before season")
			}
			for _, ids := range [][]uint64{v.CostumeBanIDs, v.BurstBanIDs} {
				if e := int32s(ids...); e != nil {
					return e
				}
				seen := map[uint64]bool{}
				for _, id := range ids {
					if id == 0 || seen[id] {
						return fmt.Errorf("invalid hunt ban identity")
					}
					seen[id] = true
				}
			}
		}
		for _, v := range h.History {
			if e := claim(fmt.Sprintf("hunt_history:%d", v.Season)); e != nil {
				return e
			}
			if v.Season == 0 || v.HuntID == 0 {
				return fmt.Errorf("missing history identity")
			}
			if e := int32s(v.Season, v.HuntID); e != nil {
				return e
			}
		}
		s.MonsterHunt = h
	}
	for _, v := range m.CashProducts {
		if e := claim(fmt.Sprintf("cash:%d:%d:%d", v.GroupID, v.ProductID, v.SaleGroup)); e != nil {
			return e
		}
		if v.GroupID == 0 || v.ProductID == 0 || v.EventIndex > math.MaxInt64 {
			return fmt.Errorf("invalid cash identity")
		}
		if e := int32s(v.GroupID, v.ProductID, v.SaleGroup, v.EndDelayMinutes); e != nil {
			return e
		}
		var a, b uint64
		var e error
		if v.Start != "" {
			a, e = ParseTimestamp(v.Start)
			if e != nil {
				return e
			}
		}
		if v.End != "" {
			b, e = ParseTimestamp(v.End)
			if e != nil {
				return e
			}
			if b <= a {
				return fmt.Errorf("invalid cash window")
			}
		}
		s.CashProducts = append(s.CashProducts, v)
	}
	return nil
}

func eventIdentity(v Event) string {
	if v.UID != 0 {
		return fmt.Sprintf("event:%d", v.UID)
	}
	return fmt.Sprintf("event:0:%d:%d:%d", v.Type, v.ID, v.SubID)
}
func eventWindow(start, end string) (int64, int64, error) {
	a, e := time.Parse(time.RFC3339Nano, start)
	if e != nil {
		return 0, 0, e
	}
	b, e := time.Parse(time.RFC3339Nano, end)
	if e != nil {
		return 0, 0, e
	}
	if a.Nanosecond()%1000000 != 0 || b.Nanosecond()%1000000 != 0 || b.UnixMilli() <= a.UnixMilli() {
		return 0, 0, fmt.Errorf("invalid event window")
	}
	return a.UnixMilli(), b.UnixMilli(), nil
}
