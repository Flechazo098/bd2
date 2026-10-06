package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestCostumeCharacterRelationshipDoesNotUseNumericPrefix(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE CostumeTable(id INTEGER,ProtoBuf BLOB);CREATE TABLE CharTable(id INTEGER,uniqueCharId INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO CostumeTable VALUES (?,?)", 76543, wire.AppendVarint(nil, 27, 88))
	for _, c := range []struct{ id, growth, temp uint64 }{{999, 1, 0}, {1000, 2, 0}, {222, 1, 4}} {
		raw := wire.AppendVarint(wire.AppendVarint(nil, 10, c.growth), 21, c.temp)
		db.Exec("INSERT INTO CharTable VALUES (?,?,?)", c.id, 88, raw)
	}
	got, err := loadCostumeBaseCharacterID(db, 76543)
	if err != nil || got != 999 {
		t.Fatalf("character=%d err=%v", got, err)
	}
	base, family, err := loadCostumeCharacterFamily(db, 76543)
	if err != nil || base != 999 || len(family) != 2 {
		t.Fatalf("base=%d family=%v err=%v", base, family, err)
	}
	seen := map[uint64]bool{}
	for _, id := range family {
		seen[id] = true
	}
	if !seen[999] || !seen[1000] || seen[222] {
		t.Fatalf("promotion family=%v", family)
	}
	db.Exec("INSERT INTO CharTable VALUES (?,?,?)", 777, 88, wire.AppendVarint(nil, 10, 1))
	if _, err := loadCostumeBaseCharacterID(db, 76543); err == nil {
		t.Fatal("ambiguous base guessed")
	}
}
