package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"reflect"
	"testing"
)

func TestBattleDeckPhasesDesignRows(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	db.SetMaxOpenConns(1)
	for _, q := range []string{"CREATE TABLE FieldMonsterTable(id INTEGER PRIMARY KEY, ProtoBuf BLOB)", "CREATE TABLE PhaseBattleTable(groupId INTEGER,id INTEGER,ProtoBuf BLOB)"} {
		if _, e := db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range []struct {
		id int
		b  []byte
	}{{8, []byte{0xa8, 1, 1}}, {1, nil}, {2, []byte{0xa8, 1, 7}}} {
		if _, e := db.Exec("INSERT INTO FieldMonsterTable VALUES (?,?)", v.id, v.b); e != nil {
			t.Fatal(e)
		}
	}
	// Rows reproduce Python-confirmed pack22 fields; group7 tests nonadjacent IDs.
	for _, v := range []struct{ g, id, d int }{{1, 2, 9}, {1, 1, 8}, {7, 50, 99}, {7, 10, 77}, {7, 30, 88}} {
		b := []byte{0x10, byte(v.d), 0x18, byte(v.g), 0x20, byte(v.id)}
		if _, e := db.Exec("INSERT INTO PhaseBattleTable VALUES (?,?,?)", v.g, v.id, b); e != nil {
			t.Fatal(e)
		}
	}
	for _, c := range []struct {
		m, d uint64
		w    []BattlePhase
	}{{8, 8, []BattlePhase{{1, 1, 8}, {1, 2, 9}}}, {8, 9, []BattlePhase{{1, 1, 8}, {1, 2, 9}}}, {1, 1, nil}, {2, 77, []BattlePhase{{7, 10, 77}, {7, 30, 88}, {7, 50, 99}}}} {
		g, e := battleDeckPhasesFromDB(db, c.m, c.d)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(g, c.w) {
			t.Fatalf("got %+v want %+v", g, c.w)
		}
	}
	if _, e := battleDeckPhasesFromDB(db, 8, 1); e == nil {
		t.Fatal("accepted unrelated deck")
	}
	for _, b := range [][]byte{{0x10, 8, 0x18, 2, 0x20, 1}, {0x10, 8, 0x18, 1, 0x20, 3}, {0x18, 1, 0x20, 1}} {
		if _, e := db.Exec("UPDATE PhaseBattleTable SET ProtoBuf=? WHERE groupId=1 AND id=1", b); e != nil {
			t.Fatal(e)
		}
		if _, e := battleDeckPhasesFromDB(db, 8, 8); e == nil {
			t.Fatalf("accepted invalid row %x", b)
		}
	}
}

func TestBattleDeckPhasesRetainSelectedDifficulty(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, q := range []string{"CREATE TABLE FieldMonsterTable(id INTEGER PRIMARY KEY,ProtoBuf BLOB)", "CREATE TABLE PhaseBattleTable(groupId INTEGER,id INTEGER,ProtoBuf BLOB)", "CREATE TABLE BattleDeckTable(id INTEGER PRIMARY KEY,ProtoBuf BLOB)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO FieldMonsterTable VALUES(8,?)", wire.AppendVarint(nil, 21, 1)); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ id, deck uint64 }{{1, 8}, {2, 9}} {
		data := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 2, v.deck), 3, 1), 4, v.id)
		if _, err := db.Exec("INSERT INTO PhaseBattleTable VALUES(1,?,?)", v.id, data); err != nil {
			t.Fatal(err)
		}
		for d := uint64(0); d <= 2; d++ {
			if _, err := db.Exec("INSERT INTO BattleDeckTable VALUES(?,?)", v.deck+d*100000, wire.AppendVarint(nil, 24, d)); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := battleDeckPhasesFromDB(db, 8, 200008)
	want := []BattlePhase{{1, 1, 200008}, {1, 2, 200009}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v error %v", got, err)
	}
}
func TestPhaseScalarRejectsInvalidDesign(t *testing.T) {
	for _, b := range [][]byte{{0x1a, 1, 1}, {0x18, 1, 0x18, 2}} {
		if _, e := phaseScalar(b, 3); e == nil {
			t.Fatalf("accepted %x", b)
		}
	}
}
