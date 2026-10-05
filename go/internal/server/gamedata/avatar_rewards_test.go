package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"math"
	"testing"
)

func TestAvatarRewardsLoadAndValidateTypedMembers(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"AvatarItemTable", "AvatarMotionTable", "AvatarCharTable", "AvatarSetTable"} {
		if _, err = db.Exec("CREATE TABLE " + table + "(ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		table string
		field int
		id    uint64
	}{{"AvatarItemTable", 3, 10001}, {"AvatarMotionTable", 5, 7}, {"AvatarCharTable", 6, 2}} {
		if _, err = db.Exec("INSERT INTO "+row.table+" VALUES(?)", wire.AppendVarint(nil, row.field, row.id)); err != nil {
			t.Fatal(err)
		}
	}
	set := wire.AppendVarint(nil, 6, 1)
	for _, member := range []BattleReward{{49, 10001, 1}, {50, 7, 1}, {61, 2, 1}} {
		set = wire.AppendVarint(set, 5, member.Type)
		set = wire.AppendVarint(set, 4, member.ID)
		set = wire.AppendVarint(set, 3, member.Count)
	}
	if _, err = db.Exec("INSERT INTO AvatarSetTable VALUES(?)", set); err != nil {
		t.Fatal(err)
	}
	d, err := loadAvatarRewardDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	leaves, err := d.Expand([]BattleReward{{Type: 62, ID: 1, Count: 2}, {Type: 4, Count: 10}})
	if err != nil || len(leaves) != 4 || leaves[0].Count != 2 || leaves[2].Type != 61 {
		t.Fatal("avatar set members malformed", leaves, err)
	}
	if _, err = d.Expand([]BattleReward{{Type: 62, ID: 99, Count: 1}}); err == nil {
		t.Fatal("unknown set accepted")
	}
	d.Sets[1][0].Count = 2
	if _, err = d.Expand([]BattleReward{{Type: 62, ID: 1, Count: math.MaxInt32}}); err == nil {
		t.Fatal("avatar quantity overflow accepted")
	}
}
