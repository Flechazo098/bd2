package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"os"
	"reflect"
	"testing"
)

func TestInstalledOverwhelmCatalogIncludesScriptedAndRegeneratingMonsters(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	rows, e := LoadFieldMonsters(root, "20260923193640", 1)
	if e != nil {
		t.Fatal(e)
	}
	script, regen := 0, 0
	for _, m := range rows {
		if m.GroupID == 0 {
			script++
		} else {
			regen++
		}
	}
	if script == 0 || regen == 0 {
		t.Fatal("catalog lost scripted or regenerating monsters", script, regen)
	}
	rules, e := LoadSkyWayOverwhelm(root, "20260923193640")
	if e != nil || len(rules) == 0 {
		t.Fatal("skyway catalog unavailable", e)
	}
	rule, e := LoadOverwhelmQuest(root, "20260923193640", 1, 1)
	if e != nil || rule.Enemies == nil {
		t.Fatal("quest battle rule catalog unavailable", e)
	}
}

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
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], FieldMonsterDesign{ID: 3, GroupID: 17, QuestID: 91, BattleDeck: 78, LifeSeconds: 30, BattleDecks: []uint64{78}}) {
		t.Fatalf("catalog %+v: %v", rows, err)
	}
	if _, err = db.Exec("UPDATE FieldMonsterTable SET ProtoBuf=?", wire.AppendVarint(nil, 24, 18)); err != nil {
		t.Fatal(err)
	}
	if _, err = loadFieldMonsters(db); err == nil {
		t.Fatal("accepted unknown regeneration group")
	}
}
