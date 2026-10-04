package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestAchievementCounterDesignUsesRootGroupsAndBothContents(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("CREATE TABLE AchievementTable (ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][3]uint64{{7, 0, 0}, {7, 0, 0}, {7, 1, 0}, {8, 0, 7}} {
		raw := wire.AppendVarint(nil, 9, row[0])
		raw = wire.AppendVarint(raw, 4, row[1])
		raw = wire.AppendVarint(raw, 13, row[2])
		if _, err = db.Exec("INSERT INTO AchievementTable VALUES (?)", raw); err != nil {
			t.Fatal(err)
		}
	}
	d, err := loadAchievementCounterDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Groups) != 1 || len(d.Groups[7]) != 2 || d.Groups[7][0] != 0 || d.Groups[7][1] != 1 {
		t.Fatalf("wrong group index: %#v", d)
	}
}
