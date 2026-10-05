package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestQuestCollectionsFollowChangedPackQuestAndItemIDs(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "quest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, sqlText := range []string{"CREATE TABLE QuestTable77(id INTEGER,ProtoBuf BLOB)", "CREATE TABLE CollectionTable(id INTEGER,ProtoBuf BLOB)"} {
		if _, err := db.Exec(sqlText); err != nil {
			t.Fatal(err)
		}
	}
	quest := wire.AppendVarint(wire.AppendVarint(nil, 6, 801), 6, 802)
	if _, err := db.Exec("INSERT INTO QuestTable77 VALUES(49,?)", quest); err != nil {
		t.Fatal(err)
	}
	for _, collectionID := range []uint64{801, 802} {
		if _, err := db.Exec("INSERT INTO CollectionTable VALUES(?,?)", collectionID, wire.AppendVarint(nil, 3, collectionID)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := loadQuestDesignDB(db, 77)
	if err != nil {
		t.Fatal(err)
	}
	rewards := got[49].CollectionRewards
	if len(rewards) != 2 || rewards[0] != (Reward{Type: 17, ID: 801, Count: 1}) || rewards[1] != (Reward{Type: 17, ID: 802, Count: 1}) {
		t.Fatalf("rewards=%+v", rewards)
	}
	if _, err := db.Exec("DELETE FROM CollectionTable WHERE id=802"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadQuestDesignDB(db, 77); err == nil {
		t.Fatal("missing collection design accepted")
	}
}
