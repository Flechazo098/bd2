package monsterhunt

import (
	"testing"
	"time"

	"bd2server/internal/server/readonly"
	"bd2server/internal/server/wire"
)

func TestFutureSeasonDoesNotReplaceActiveOrRewardSeason(t *testing.T) {
	rows := []season{
		{ID: 5, Start: 100, End: 200},
		{ID: 3, Start: 300, End: 400},
		{ID: 999, Start: 500, End: 600},
	}
	for _, tc := range []struct{ now, want uint64 }{
		{50, 5}, {100, 5}, {199, 5}, {200, 5}, {299, 5},
		{300, 3}, {400, 3}, {500, 999},
	} {
		if got := selectSeason(rows, tc.now); got.ID != tc.want {
			t.Fatalf("time=%d season=%d want=%d", tc.now, got.ID, tc.want)
		}
	}
}

func TestSchedulePreservesAllCalendarsAndPlacesSelectedCategoriesLast(t *testing.T) {
	field := func(n int, v uint64) readonly.Field { return readonly.Field{Number: n, Type: 0, Varint: v} }
	row := func(id uint64, independent bool) readonly.Field {
		flag := uint64(0)
		if independent {
			flag = 1
		}
		return readonly.Field{Number: 1, Type: 2, Fields: []readonly.Field{
			{Number: 1, Type: 2, Fields: []readonly.Field{field(1, id)}}, field(6, flag),
		}}
	}
	s := &Service{now: func() time.Time { return time.UnixMilli(150) },
		seasons: []season{{ID: 1, Start: 100, End: 200}, {ID: 2, Start: 300, End: 400}, {ID: 3, Start: 100, End: 200, Independent: true}, {ID: 4, Start: 300, End: 400, Independent: true}},
		seed: &readonly.Seed{Responses: map[string]readonly.Response{"/MonsterHuntScheduleInfo": {
			Fields: []readonly.Field{row(1, false), row(2, false), row(3, true), row(4, true), field(2, 5), {Number: 3, Type: 2, Fields: []readonly.Field{field(1, 77)}}},
		}}},
	}
	for _, now := range []int64{50, 150, 250, 350, 450} {
		s.now = func() time.Time { return time.UnixMilli(now) }
		_, response, _, err := s.scheduleInfo(wire.AppendVarint(nil, 1, 1))
		if err != nil {
			t.Fatal(err)
		}
		var ids []uint64
		var lastRegular, lastIndependent uint64
		history := 0
		if err := wire.Walk(response, func(f wire.Field) error {
			if f.Number == 1 {
				nested, _, _ := wire.Bytes(f.Value, 1)
				id, _, _ := wire.Varint(nested, 1)
				ids = append(ids, id)
				flag, _, _ := wire.Varint(f.Value, 6)
				if flag == 0 {
					lastRegular = id
				} else {
					lastIndependent = id
				}
			}
			if f.Number == 3 {
				history++
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		want := uint64(1)
		if now >= 300 {
			want = 2
		}
		seen := map[uint64]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		if len(ids) != 4 || len(seen) != 4 || !seen[1] || !seen[2] || !seen[3] || !seen[4] || lastRegular != want || lastIndependent != want+2 || history != 1 {
			t.Fatalf("time=%d rows=%v history=%d", now, ids, history)
		}
		if len(s.seed.Responses["/MonsterHuntScheduleInfo"].Fields) != 6 {
			t.Fatal("projection mutated published calendars")
		}
	}
}
