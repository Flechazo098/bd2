package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestPackSummaryTargetsMatchClientCategories(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE PackTable(id INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	// Client targets: story, character, evil castle, event, square, master story.
	targetTypes := map[int]bool{0: true, 1: true, 4: true, 6: true, 11: true, 1000: true}
	for _, typ := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 100, 1000} {
		if _, err := db.Exec("INSERT INTO PackTable VALUES(?,?)", typ+1, wire.AppendVarint(nil, 55, uint64(typ))); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := loadPackSummaryTargets(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != len(targetTypes) {
		t.Fatalf("unexpected targets %v", targets)
	}
	for typ := range targetTypes {
		if !targets[typ+1] {
			t.Fatalf("missing client target type %d", typ)
		}
	}
	if _, err := db.Exec("INSERT INTO PackTable VALUES(2000,?)", wire.AppendVarint(wire.AppendVarint(nil, 55, 1), 55, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPackSummaryTargets(db); err == nil {
		t.Fatal("duplicate scalar pack type accepted")
	}
}
