package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestPackJamDesignLoadsDefaultsAndRejectsUnsupportedRewards(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE PackJamEventTable(id INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	raw := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 2, 5), 3, 5), 4, 150), 6, 3)
	if _, err := db.Exec("INSERT INTO PackJamEventTable VALUES(0,?)", raw); err != nil {
		t.Fatal(err)
	}
	design, err := loadPackJamDesign(db)
	if err != nil || design.InsertMin != 5 || design.InsertMax != 5 || design.Reward.ID != 0 || design.Reward.Type != 3 || design.Reward.Count != 150 {
		t.Fatalf("design=%+v err=%v", design, err)
	}
	raw = wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 2, 5), 3, 5), 4, 150), 6, 4)
	if _, err := db.Exec("UPDATE PackJamEventTable SET ProtoBuf=?", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPackJamDesign(db); err == nil {
		t.Fatal("unsupported currency accepted")
	}
}
