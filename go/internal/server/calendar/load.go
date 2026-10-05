package calendar

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gacha"
	"bd2server/internal/server/schedule"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func timestamp(raw string) (uint64, error) {
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
	a, e := timestamp(start)
	if e != nil {
		return 0, 0, e
	}
	b, e := timestamp(end)
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

// LoadDirectory validates the whole directory before returning any calendar.
// Every calendar file must use the .bd2schedule suffix.
// Directories and symlinks are
// rejected so an accidentally omitted calendar never produces a partial set.
func LoadDirectory(dir, gameVersion, gameDataVersion string) (*Set, error) {
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, fmt.Errorf("calendar: read directory: %w", e)
	}
	set := &Set{GachaSeed: &gacha.ScheduleSeed{ClientVersion: gameVersion}}
	seen := map[string]bool{}
	claim := func(key string) error {
		if seen[key] {
			return fmt.Errorf("duplicate calendar identity %s", key)
		}
		seen[key] = true
		return nil
	}
	files := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".bd2schedule") {
			return nil, fmt.Errorf("calendar: unsupported entry %s", entry.Name())
		}
		info, e := entry.Info()
		if e != nil {
			return nil, e
		}
		if info.Size() > MaxFileSize {
			return nil, fmt.Errorf("calendar: %s: file size limit exceeded", entry.Name())
		}
		raw, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			return nil, e
		}
		m, e := UnmarshalBinary(raw)
		if e != nil {
			return nil, fmt.Errorf("calendar: %s: %w", entry.Name(), e)
		}
		if m.SchemaVersion != 1 || strings.TrimSpace(m.Revision) == "" || m.GameVersion != gameVersion || m.GameDataVersion != gameDataVersion || gameVersion == "" || gameDataVersion == "" {
			return nil, fmt.Errorf("calendar: %s: incompatible schema/version or missing revision", entry.Name())
		}
		if e = merge(set, m, claim); e != nil {
			return nil, fmt.Errorf("calendar: %s: %w", entry.Name(), e)
		}
		set.Revisions = append(set.Revisions, m.Revision)
		files++
	}
	if files == 0 {
		return nil, fmt.Errorf("calendar: empty directory")
	}
	sort.Slice(set.EventHubs, func(i, j int) bool { return set.EventHubs[i].UID < set.EventHubs[j].UID })
	sort.Slice(set.MiniGameHubs, func(i, j int) bool { return set.MiniGameHubs[i].Slot < set.MiniGameHubs[j].Slot })
	sort.Slice(set.Events, func(i, j int) bool {
		a, b := set.Events[i], set.Events[j]
		if a.UID != b.UID {
			return a.UID < b.UID
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.SubID < b.SubID
	})
	sortWindows := func(w []gacha.ScheduleWindow) {
		sort.Slice(w, func(i, j int) bool {
			if w[i].GroupID != w[j].GroupID {
				return w[i].GroupID < w[j].GroupID
			}
			return w[i].StartTime < w[j].StartTime
		})
	}
	sortWindows(set.GachaSeed.Schedules)
	sortWindows(set.GachaSeed.StepUps)
	if e = set.GachaSeed.Validate(gameVersion); e != nil {
		return nil, e
	}
	if set.RegularService != nil {
		sort.Slice(set.RegularService.Contents, func(i, j int) bool { return set.RegularService.Contents[i].ID < set.RegularService.Contents[j].ID })
		sort.Slice(set.RegularService.Regular, func(i, j int) bool {
			return set.RegularService.Regular[i].ContentID < set.RegularService.Regular[j].ContentID
		})
		if e = set.RegularService.Validate(); e != nil {
			return nil, e
		}
	}
	sort.Slice(set.CashProducts, func(i, j int) bool {
		a, b := set.CashProducts[i], set.CashProducts[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.ProductID != b.ProductID {
			return a.ProductID < b.ProductID
		}
		return a.SaleGroup < b.SaleGroup
	})
	if set.MonsterHunt != nil {
		sort.Slice(set.MonsterHunt.Seasons, func(i, j int) bool {
			return set.MonsterHunt.Seasons[i].Season.ID < set.MonsterHunt.Seasons[j].Season.ID
		})
		sort.Slice(set.MonsterHunt.History, func(i, j int) bool { return set.MonsterHunt.History[i].Season < set.MonsterHunt.History[j].Season })
	}
	return set, nil
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
		play, e := timestamp(v.PlayEnd)
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
			end, e := timestamp(v.CalculateEndAt)
			if e != nil {
				return e
			}
			seasonEnd, _ := timestamp(v.Season.End)
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
			a, e = timestamp(v.Start)
			if e != nil {
				return e
			}
		}
		if v.End != "" {
			b, e = timestamp(v.End)
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
