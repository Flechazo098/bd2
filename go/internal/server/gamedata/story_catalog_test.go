package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestStoryCatalogEnumeratesSeparateChainsAndContentTicketRules(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{"CREATE TABLE PackTable(id INTEGER PRIMARY KEY,ProtoBuf BLOB)", "CREATE TABLE ContentOpenTable(groupId INTEGER,id INTEGER,ProtoBuf BLOB)", "CREATE TABLE QuestTable1(id INTEGER PRIMARY KEY,ProtoBuf BLOB)", "CREATE TABLE QuestTable7(id INTEGER PRIMARY KEY,ProtoBuf BLOB)", "CREATE TABLE QuestTable8(id INTEGER PRIMARY KEY,ProtoBuf BLOB)"} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, pack := range []struct{ id, kind, next int }{{1, 0, 0}, {7, 1000, 8}, {8, 1000, 0}, {9, 2, 0}} {
		raw := wire.AppendVarint(nil, 25, uint64(pack.id))
		raw = wire.AppendVarint(raw, 55, uint64(pack.kind))
		raw = wire.AppendVarint(raw, 45, uint64(pack.next))
		raw = wire.AppendVarint(raw, 21, uint64(pack.id*10))
		if pack.id == 7 {
			raw = wire.AppendVarint(raw, 8, 1)
			raw = wire.AppendVarint(raw, 9, 100)
			raw = wire.AppendVarint(raw, 10, 19)
			// Non-stackable costume instances retain their ID with count zero.
			raw = wire.AppendVarint(raw, 8, 0)
			raw = wire.AppendVarint(raw, 9, 200)
			raw = wire.AppendVarint(raw, 10, 11)
		}
		if _, err = db.Exec("INSERT INTO PackTable VALUES(?,?)", pack.id, raw); err != nil {
			t.Fatal(err)
		}
	}
	raw := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1), 6, 100)
	raw = wire.AppendVarint(raw, 5, 3)
	raw = wire.AppendVarint(raw, 4, 99)
	if _, err = db.Exec("INSERT INTO ContentOpenTable VALUES(1,1,?)", raw); err != nil {
		t.Fatal(err)
	}
	main := wire.AppendVarint(wire.AppendVarint(nil, 35, 2), 37, 0)
	if _, err = db.Exec("INSERT INTO QuestTable1 VALUES(1,?)", main); err != nil {
		t.Fatal(err)
	}
	terminal := wire.AppendVarint(nil, 37, 1)
	if _, err = db.Exec("INSERT INTO QuestTable1 VALUES(2,?)", terminal); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO QuestTable1 VALUES(3,?)", wire.AppendVarint(nil, 63, 1)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{7, 8} {
		query := "INSERT INTO QuestTable7 VALUES(1,?)"
		if id == 8 {
			query = "INSERT INTO QuestTable8 VALUES(1,?)"
		}
		raw = wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 46, 1), 51, 101), 56, 19)
		if _, err = db.Exec(query, raw); err != nil {
			t.Fatal(err)
		}
	}
	d, err := loadStoryCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Packs) != 3 || d.Packs[8].NextPackID != 0 || d.Packs[7].NextPackID != 8 {
		t.Fatalf("catalog=%+v", d.Packs)
	}
	if open := d.Packs[1].Open; open == nil || open.TicketID != 100 || open.SquadLevel != 3 || open.TutorialID != 99 {
		t.Fatalf("open=%+v", open)
	}
	if d.Packs[7].Open != nil {
		t.Fatal("invented missing content-open permission rule")
	}
	if main := d.Packs[1].MainQuestIDs; len(main) != 2 || main[0] != 1 || main[1] != 2 {
		t.Fatalf("sidequest in chapter completion=%v", main)
	}
	if d.Packs[1].Quests[1].NextQuestID != 2 || d.Packs[1].Quests[2].PriorQuestID != 1 || d.Packs[1].Quests[3].Type != 1 {
		t.Fatal("quest edges/type were lost")
	}
	if rewards := d.Packs[7].BuyRewards; len(rewards) != 2 || rewards[0].Type != 19 || rewards[0].ID != 100 || rewards[1].Type != 11 || rewards[1].ID != 200 || rewards[1].Count != 0 {
		t.Fatalf("buy rewards=%+v", rewards)
	}
	if len(d.TicketSources[101]) != 2 {
		t.Fatalf("ticket quest sources=%+v", d.TicketSources)
	}
}
