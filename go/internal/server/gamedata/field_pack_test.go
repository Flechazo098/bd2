package gamedata

import (
	"database/sql"
	"testing"

	"bd2server/internal/server/wire"
)

func TestFieldPacksLoadMetadataMapsAndEntryRestrictions(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, query := range []string{"CREATE TABLE PackTable(id INTEGER,ProtoBuf BLOB)", "CREATE TABLE ContentOpenTable(groupId INTEGER,id INTEGER,ProtoBuf BLOB)", "CREATE TABLE MapTable(id INTEGER,packId INTEGER)"} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, typ uint64 }{{901, 3}, {902, 10}, {903, 0}} {
		proto := wire.AppendVarint(wire.AppendVarint(nil, 25, row.id), 55, row.typ)
		if row.id == 902 {
			proto = wire.AppendVarint(proto, 7, 20)
			proto = wire.AppendVarint(proto, 65, 1)
		}
		if _, err := db.Exec("INSERT INTO PackTable VALUES(?,?)", row.id, proto); err != nil {
			t.Fatal(err)
		}
	}
	opening := wire.AppendVarint(wire.AppendVarint(nil, 5, 6), 6, 555)
	if _, err := db.Exec("INSERT INTO ContentOpenTable VALUES(1,901,?)", opening); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO MapTable VALUES(9011,901),(9012,901),(9021,902),(9031,903)"); err != nil {
		t.Fatal(err)
	}
	packs, err := loadFieldPacks(db)
	if err != nil {
		t.Fatal(err)
	}
	first := packs[901]
	if first.ID != 901 || first.Type != 3 || first.TicketID != 555 || first.SquadLevel != 6 || !first.HasOpenRule || !first.MapIDs[9011] || first.MapIDs[9031] {
		t.Fatalf("arena metadata=%+v", first)
	}
	second := packs[902]
	if second.BuyPrice != 20 || second.UseSchedule != 1 || len(second.MapIDs) != 1 {
		t.Fatalf("other arena=%+v", second)
	}
	if _, found := packs[903]; found {
		t.Fatal("story loaded as arena")
	}
	if _, err := db.Exec("DELETE FROM MapTable WHERE packId=902"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFieldPacks(db); err == nil {
		t.Fatal("arena without valid map accepted")
	}
}
