package gamedata

import (
	"os"
	"testing"
)

func TestEventGameInstalledRules(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	for _, kind := range []uint64{12, 13, 17, 19} {
		d, e := LoadEventGame(root, "20260923193640", kind, 1)
		if e != nil {
			t.Fatalf("kind=%d %v", kind, e)
		}
		if d.Type != kind || d.Cost == 0 || len(d.Cells) == 0 {
			t.Fatalf("kind=%d invalid %+v", kind, d)
		}
		if kind == 12 && len(d.Moves) == 0 {
			t.Fatal("no moves")
		}
		if kind == 13 && len(d.Lines) == 0 {
			t.Fatal("no lines")
		}
		if kind == 17 && len(d.Complete) == 0 {
			t.Fatal("no words")
		}
	}
}
