package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestRecruitNPCResolvesScoutParallelInteractionWithZeroPackField(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE FieldNpcTable(id INTEGER PRIMARY KEY,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	raw := wire.AppendVarint(nil, 8, 7)
	raw = wire.AppendVarint(raw, 14, 9)
	for _, value := range []uint64{1, 12, 4} {
		raw = wire.AppendVarint(raw, 9, value)
	}
	for _, value := range []uint64{0, 30, 40} {
		raw = wire.AppendVarint(raw, 12, value)
	}
	raw = wire.AppendVarint(raw, 18, 1)
	raw = wire.AppendVarint(raw, 19, 2)
	if _, err = db.Exec("INSERT INTO FieldNpcTable VALUES(7,?)", raw); err != nil {
		t.Fatal(err)
	}
	npc, err := loadRecruitNPC(db, 7)
	if err != nil || npc.ScoutID != 40 || npc.MapID != 9 || npc.QuestEnableTypes[0] != 1 {
		t.Fatalf("npc=%+v err=%v", npc, err)
	}
	if _, err = loadRecruitNPC(db, 8); err == nil {
		t.Fatal("accepted absent NPC")
	}
	if _, err = db.Exec("CREATE TABLE QuestTable1(id INTEGER PRIMARY KEY,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO QuestTable1 VALUES(2,?)", wire.AppendVarint(nil, 63, 1)); err != nil {
		t.Fatal(err)
	}
	if err = loadRecruitNPCQuestTypes(db, 1, &npc); err != nil || npc.QuestTypes[2] != 1 {
		t.Fatalf("questtypes=%v err=%v", npc.QuestTypes, err)
	}
}
