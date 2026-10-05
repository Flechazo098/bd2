package eventtasks

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"testing"
)

func TestZeroUIDMissionGroupsKeepIndependentClaimsAfterRestart(t *testing.T) {
	s, economy, store := setup(t)
	now := s.now().UnixMilli()
	rows := []events.Schedule{{Type: 4, ID: 7, Start: now - 1, End: now + 10000}, {Type: 4, ID: 8, Start: now - 1, End: now + 10000}}
	if err := s.registry.(*events.Registry).Replace(rows); err != nil {
		t.Fatal(err)
	}
	s.design.MissionGroups[8] = gamedata.EventMissionGroup{ID: 8, Groups: []uint64{9}}
	if err := s.RecordEvent(2, 0, 2, nil); err != nil {
		t.Fatal(err)
	}
	claim := func(seq, group uint64) []byte {
		b := wire.AppendVarint(req(seq), 3, 2)
		b = wire.AppendVarint(b, 4, 9)
		b = wire.AppendVarint(b, 5, 10)
		return wire.AppendVarint(b, 6, group)
	}
	if _, _, _, err := s.Handle("/MissionClear", claim(1, 7)); err != nil {
		t.Fatal(err)
	}
	if !s.mission(rows[0], 10).Claimed || s.mission(rows[1], 10).Claimed {
		t.Fatal("claim leaked across zero-UID groups")
	}
	restarted, err := Open(store, s.design, s.registry, economy)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = s.now
	if !restarted.mission(rows[0], 10).Claimed || restarted.mission(rows[1], 10).Claimed {
		t.Fatal("independent claims lost after restart")
	}
	if _, _, _, err := restarted.Handle("/MissionClear", claim(2, 8)); err != nil {
		t.Fatal(err)
	}
	if economy.calls != 2 {
		t.Fatalf("reward grants=%d", economy.calls)
	}
}

func TestInactiveCalendarDoesNotHideActivePassMissions(t *testing.T) {
	s, _, _ := setup(t)
	now := s.now().UnixMilli()
	if err := s.registry.(*events.Registry).Replace([]events.Schedule{
		{UID: 2, Type: 4, ID: 7, Start: now - 20, End: now - 10},
		{UID: 3, Type: 5, ID: 8, Start: now - 1, End: now + 10000},
		{UID: 4, Type: 4, ID: 9, Start: now + 10000, End: now + 20000},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvent(2, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	_, out, _, err := s.Handle("/EventMissionInfo", req(1))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := wire.Walk(out, func(f wire.Field) error {
		if f.Number == 1 {
			count++
			if scalar(f.Value, 1) != 7 || scalar(f.Value, 4) != 1 {
				t.Fatalf("unexpected mission entry %x", f.Value)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("active mission count=%d", count)
	}
}
