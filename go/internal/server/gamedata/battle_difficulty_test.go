package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"os"
	"testing"
)

func TestBattleDifficultyUsesExistingDesignRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE BattleDeckTable(id INTEGER PRIMARY KEY, ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, d uint64 }{{1, 0}, {100001, 1}, {200001, 2}, {7, 0}, {100007, 2}} {
		if _, err := db.Exec("INSERT INTO BattleDeckTable VALUES(?,?)", row.id, wire.AppendVarint(nil, 24, row.d)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ id, d, w uint64 }{{1, 0, 1}, {1, 1, 100001}, {1, 2, 200001}, {100001, 1, 100001}} {
		got, err := battleDeckForDifficultyFromDB(db, c.id, c.d)
		if err != nil || got != c.w {
			t.Fatalf("%+v got %d: %v", c, got, err)
		}
	}
	for _, c := range []struct{ id, d uint64 }{{1, 3}, {7, 1}, {7, 2}, {100001, 2}, {100001, 0}} {
		if _, err := battleDeckForDifficultyFromDB(db, c.id, c.d); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

func TestInstalledStoryBattleDifficultyDecks(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	// Python read-only inspection of current BattleDeckTable confirmed these
	// independently authored rows and their field 24 difficulty IDs.
	for _, pack := range []int{1, 21} {
		for difficulty := uint64(0); difficulty <= 2; difficulty++ {
			want := uint64(1) + difficulty*100000
			got, err := BattleDeckForDifficulty(root, "20260923193640", pack, 1, difficulty)
			if err != nil || got != want {
				t.Fatalf("pack%d difficulty%d got %d: %v", pack, difficulty, got, err)
			}
		}
	}
}
