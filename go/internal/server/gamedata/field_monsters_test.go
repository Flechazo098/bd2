package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestFieldMonsterCatalogJoinsRegenerationAndRejectsBrokenReference(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"FieldMonsterTable", "FieldMonsterRegenTable"} {
		if _, err = db.Exec("CREATE TABLE " + table + " (id INTEGER,ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	regen := wire.AppendVarint(wire.AppendVarint(nil, 6, 91), 3, 30)
	if _, err = db.Exec("INSERT INTO FieldMonsterRegenTable VALUES (?,?)", 17, regen); err != nil {
		t.Fatal(err)
	}
	monster := wire.AppendVarint(wire.AppendVarint(nil, 24, 17), 2, 78)
	if _, err = db.Exec("INSERT INTO FieldMonsterTable VALUES (?,?)", 3, monster); err != nil {
		t.Fatal(err)
	}
	rows, err := loadFieldMonsters(db)
	if err != nil || len(rows) != 1 || rows[0] != (FieldMonsterDesign{ID: 3, GroupID: 17, QuestID: 91, BattleDeck: 78, LifeSeconds: 30}) {
		t.Fatalf("catalog %+v: %v", rows, err)
	}
	if _, err = db.Exec("UPDATE FieldMonsterTable SET ProtoBuf=?", wire.AppendVarint(nil, 24, 18)); err != nil {
		t.Fatal(err)
	}
	if _, err = loadFieldMonsters(db); err == nil {
		t.Fatal("accepted unknown regeneration group")
	}
}
