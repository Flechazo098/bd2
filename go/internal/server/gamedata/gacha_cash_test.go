package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestCashRewardsUseItemCountAndRejectRandomIndependentRolls(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE RewardGroupTable(id INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	makeRaw := func(dropType, dropCount, count, ratio, typ uint64) []byte {
		raw := wire.AppendVarint(nil, 1, dropCount)
		raw = wire.AppendVarint(raw, 2, dropType)
		for _, field := range []struct {
			n int
			v uint64
		}{{4, count}, {5, 9876}, {6, typ}, {8, ratio}} {
			raw = wire.AppendVarint(raw, field.n, field.v)
		}
		return raw
	}
	for _, tc := range []struct {
		id, typ, dropType, dropCount, count, ratio, want uint64
		valid                                            bool
	}{
		{1, 19, 0, 2, 7, 1, 14, true}, {2, 8, 1, 0, 7, 100, 7, true}, {3, 8, 1, 0, 7, 35, 0, false}, {4, 10, 0, 1, 1, 1, 0, false}, {5, 4, 0, 1, 1, 1, 0, false},
	} {
		if _, err := db.Exec("INSERT INTO RewardGroupTable VALUES (?,?)", tc.id, makeRaw(tc.dropType, tc.dropCount, tc.count, tc.ratio, tc.typ)); err != nil {
			t.Fatal(err)
		}
		got, err := deterministicGachaCashRewards(db, tc.id, map[uint64]bool{})
		if (err == nil) != tc.valid {
			t.Fatalf("id=%d valid=%v err=%v", tc.id, tc.valid, err)
		}
		if tc.valid && (len(got) != 1 || got[0].Count != tc.want) {
			t.Fatalf("id=%d count=%+v want=%d", tc.id, got, tc.want)
		}
	}
}

func TestInfiniteRewardProgramDoesNotRequireEveryRarity(t *testing.T) {
	d := &InfiniteGachaDesign{Count: 2, rewardProgram: &CostumeRewardGroup{ID: 1, DropCount: 2, Entries: []CostumeRewardEntry{{ItemType: 11, ItemID: 5001, Count: 1, Weight: 1}}}}
	got, err := d.rollWith(func(uint64) (uint64, error) { return 0, nil })
	if err != nil || len(got) != 2 || got[0] != 5001 {
		t.Fatalf("roll=%v err=%v", got, err)
	}
}
