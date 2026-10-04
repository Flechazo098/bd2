package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestPackDetailDesignOnlyIncludesClientRegenRewardCategory(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE FieldMonsterTable(id INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ id, regen, typ, skip int }{{1, 0, 0, 0}, {2, 1, 0, 0}, {3, 0, 0, 1}, {4, 1, 3, 1}, {5, 1, 0, 1}, {6, 1, 1, 1}, {7, 1, 0, 2}} {
		raw := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 24, uint64(r.regen)), 30, uint64(r.typ)), 31, uint64(r.skip))
		if _, err := db.Exec("INSERT INTO FieldMonsterTable VALUES(?,?)", r.id, raw); err != nil {
			t.Fatal(err)
		}
	}
	design, err := loadPackDetailDesign(db)
	if err != nil || len(design.RegenMonsterIDs) != 2 || design.RegenMonsterIDs[0] != 5 || design.RegenMonsterIDs[1] != 6 {
		t.Fatalf("design=%+v err=%v", design, err)
	}
	if _, err := db.Exec("INSERT INTO FieldMonsterTable VALUES(8,?)", wire.AppendVarint(wire.AppendVarint(nil, 24, 1), 24, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPackDetailDesign(db); err == nil {
		t.Fatal("duplicate scalar accepted")
	}
}
