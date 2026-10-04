package gamedata

import (
	"testing"
	"time"
)

func TestFieldResetUsesConfiguredBoundary(t *testing.T) {
	s := FieldResetSchedule{DailyReset: 9 * time.Hour, WeeklyDay: time.Monday}
	before := time.Date(2026, 10, 4, 23, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	for _, r := range []int{0, 3} {
		a, _ := s.Period(r, before)
		b, _ := s.Period(r, after)
		if a == b {
			t.Fatalf("reset%d did not advance at official boundary %s", r, a)
		}
	}
	s.DailyReset = 10 * time.Hour
	a, _ := s.Period(0, after)
	b, _ := s.Period(0, after.Add(time.Hour))
	if a == b {
		t.Fatal("changed design reset ignored")
	}
	once, _ := s.Period(1, after)
	if once != "once" {
		t.Fatal(once)
	}
	if _, err := s.Period(2, after); err == nil {
		t.Fatal("invented event reset")
	}
}
func TestFieldWeightedDrawCountAndZeroWeight(t *testing.T) {
	o := FieldRewardObject{DropCount: 2, Rewards: []BattleReward{{Type: 5, ID: 1, Count: 142}, {Type: 5, ID: 2, Count: 71}}, Ratios: []uint64{0, 100}}
	for i := 0; i < 20; i++ {
		got, err := o.Draw()
		if err != nil || len(got) != 2 || got[0].ID != 2 || got[1].ID != 2 {
			t.Fatalf("draw=%+v err=%v", got, err)
		}
	}
	o.DropType = 1
	if _, err := o.Draw(); err == nil {
		t.Fatal("unsupported independent distribution accepted")
	}
}
