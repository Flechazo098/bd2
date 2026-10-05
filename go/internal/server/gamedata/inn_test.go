package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestInnLoaderJoinsNPCRecoveryMapAndReputation(t *testing.T) {
	pack, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	shared, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	for _, table := range []string{"FieldNpcTable", "ReputationGroupTable"} {
		if _, err = pack.Exec("CREATE TABLE " + table + " (id INTEGER, ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"CharRecoveryTable", "MapTable"} {
		if _, err = shared.Exec("CREATE TABLE " + table + " (id INTEGER, ProtoBuf BLOB)"); err != nil {
			t.Fatal(err)
		}
	}
	npc := wire.AppendBytes(nil, 9, []byte{2})
	npc = wire.AppendVarint(npc, 14, 88)
	npc = wire.AppendVarint(npc, 21, 4)
	if _, err = pack.Exec("INSERT INTO FieldNpcTable VALUES(?,?)", 7, npc); err != nil {
		t.Fatal(err)
	}
	if _, err = pack.Exec("INSERT INTO ReputationGroupTable VALUES(?,?)", 3, wire.AppendVarint(nil, 2, 15)); err != nil {
		t.Fatal(err)
	}
	if _, err = shared.Exec("INSERT INTO MapTable VALUES(?,?)", 88, wire.AppendVarint(nil, 18, 3)); err != nil {
		t.Fatal(err)
	}
	recovery := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 3, 2), 4, 4), 5, 20)
	if _, err = shared.Exec("INSERT INTO CharRecoveryTable VALUES(?,?)", 4, recovery); err != nil {
		t.Fatal(err)
	}
	rules, err := loadInns(pack, shared)
	if err != nil || len(rules) != 1 || rules[0] != (InnRule{NPCID: 7, MapID: 88, MapGroup: 3, Currency: 4, ItemCount: 2, FreeSquadLevel: 20, GoodDiscount: 15}) {
		t.Fatalf("joined inn %+v: %v", rules, err)
	}
	if _, err = shared.Exec("DELETE FROM CharRecoveryTable"); err != nil {
		t.Fatal(err)
	}
	if _, err = loadInns(pack, shared); err == nil {
		t.Fatal("missing recovery foreign key accepted")
	}
}
