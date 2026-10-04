package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
	"time"
)

func TestFieldObjectLoadIndependentEquipmentAndRandomBox(t *testing.T) {
	pack, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	common, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer common.Close()
	for _, table := range []string{"FieldRewardObjectGroupTable", "FieldRewardObjectTable"} {
		if _, err := pack.Exec("CREATE TABLE " + table + "(id INTEGER, ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"RewardGroupTable", "EquipmentTable"} {
		if _, err := common.Exec("CREATE TABLE " + table + "(id INTEGER, ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := common.Exec("CREATE TABLE EquipmentOptionTable(GroupId INTEGER, id INTEGER, ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	group := wire.AppendVarint(nil, 10, 66)
	group = wire.AppendVarint(group, 12, 8) // A newly introduced object kind must not need an allowlist.
	if _, err := pack.Exec("INSERT INTO FieldRewardObjectGroupTable VALUES(?,?)", 55, group); err != nil {
		t.Fatal(err)
	}
	object := wire.AppendVarint(nil, 3, 55)
	object = wire.AppendVarint(object, 6, 44)
	if _, err := pack.Exec("INSERT INTO FieldRewardObjectTable VALUES(?,?)", 33, object); err != nil {
		t.Fatal(err)
	}
	loot := wire.AppendVarint(nil, 2, 1)
	for _, r := range []BattleReward{{Type: 10, ID: 77, Count: 2}, {Type: 9, ID: 88, Count: 1}} {
		loot = wire.AppendVarint(loot, 4, r.Count)
		loot = wire.AppendVarint(loot, 5, r.ID)
		loot = wire.AppendVarint(loot, 6, r.Type)
		loot = wire.AppendVarint(loot, 8, 100)
	}
	if _, err := common.Exec("INSERT INTO RewardGroupTable VALUES(?,?)", 66, loot); err != nil {
		t.Fatal(err)
	}
	equip := wire.AppendVarint(nil, 3, 3)
	equip = wire.AppendVarint(equip, 12, 99)
	if _, err := common.Exec("INSERT INTO EquipmentTable VALUES(?,?)", 77, equip); err != nil {
		t.Fatal(err)
	}
	option := wire.AppendVarint(nil, 5, 7)
	option = wire.AppendVarint(option, 2, 100)
	if _, err := common.Exec("INSERT INTO EquipmentOptionTable VALUES(?,?,?)", 99, 7, option); err != nil {
		t.Fatal(err)
	}
	d, err := loadFieldObjects(pack, common)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Objects[33].Draw()
	if err != nil || len(got) != 2 || got[0].ID != 77 || got[0].Count != 2 || got[1].Type != 9 || got[1].ID != 88 {
		t.Fatalf("rewards=%+v err=%v", got, err)
	}
	main, _, _, err := d.Equipment.RollOptions(77)
	if err != nil || len(main) != 1 || main[0].GroupID != 99 || main[0].ID != 7 {
		t.Fatalf("options=%+v err=%v", main, err)
	}
}

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
	o.DropCount = 0
	got, err := o.Draw()
	if err != nil || len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("independent draw=%+v err=%v", got, err)
	}
}

func TestFieldDrawRejectsMalformedUnselectedBranch(t *testing.T) {
	o := FieldRewardObject{DropCount: 1, Rewards: []BattleReward{{Type: 5, ID: 1, Count: 1}, {Type: 5, ID: 2}}, Ratios: []uint64{100, 0}}
	if _, err := o.Draw(); err == nil {
		t.Fatal("zero-weight malformed reward accepted")
	}
	o.Rewards[1].Count = 1
	o.DropType = 1
	o.Ratios[1] = 101
	if _, err := o.Draw(); err == nil {
		t.Fatal("independent percentage above 100 accepted")
	}
}

func TestFieldIndependentDrawUsesEveryPercentageOnce(t *testing.T) {
	o := FieldRewardObject{DropType: 1, DropCount: 99, Ratios: []uint64{0, 25, 25, 100}, Rewards: []BattleReward{{ID: 1, Count: 1}, {ID: 2, Count: 1}, {ID: 3, Count: 1}, {ID: 4, Count: 1}}}
	values := []uint64{24, 25}
	calls := 0
	got, err := o.draw(func(limit uint64) (uint64, error) {
		if limit != 100 || calls >= len(values) {
			t.Fatalf("unexpected draw limit=%d calls=%d", limit, calls)
		}
		v := values[calls]
		calls++
		return v, nil
	})
	if err != nil || calls != 2 || len(got) != 2 || got[0].ID != 2 || got[1].ID != 4 {
		t.Fatalf("independent percentage boundary: got=%+v calls=%d err=%v", got, calls, err)
	}
}
