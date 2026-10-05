package gamedata

import (
	"os"
	"testing"
	"time"
)

func TestRealLimitedCostumesReleasesRowsBeforeCharacterQueries(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	type result struct {
		catalog *LimitedCostumeCatalog
		err     error
	}
	done := make(chan result, 1)
	go func() { c, e := LoadLimitedCostumes(root, "20260923193640"); done <- result{c, e} }()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		ids := r.catalog.IDs()
		if len(ids) == 0 {
			t.Fatal("empty limited costume catalog")
		}
		for i, id := range ids {
			if i > 0 && ids[i-1] >= id {
				t.Fatal("unstable/duplicate membership order")
			}
			design, ok := r.catalog.Character(id)
			if !ok || design.ID == 0 {
				t.Fatalf("limited costume %d missing character design", id)
			}
		}
	case <-time.After(15 * time.Second):
		t.Fatal("limited costume nested queries blocked on retained result rows")
	}
}
