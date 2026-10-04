package stateio_test

import (
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/stateio"
)

func TestOptionalGameplaySnapshotsPreserveAccountInitializationOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	domains := []string{"characters", "collection", "deck", "equipment", "items", "mail", "missions", "progress", "wallet"}
	for _, name := range domains {
		if err := repo.Save(name, []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.MarkInitializationComplete(); err != nil {
		t.Fatal(err)
	}
	store := stateio.EntrySnapshotStore{Entries: repo, Domain: "missions", Bucket: "gameplay"}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hunting", "huntdispatch", "monsterhunt"} {
		if err := store.Save(name, []byte("{\"version\":\"2.35.10\"}")); err != nil {
			_ = op.Rollback()
			t.Fatal(err)
		}
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if status, err := reopened.InitializationState(domains...); err != nil || status != accountstate.InitializationComplete {
		t.Fatalf("restart rejected optional gameplay state: %d %v", status, err)
	}
	store.Entries = reopened
	for _, name := range []string{"hunting", "huntdispatch", "monsterhunt"} {
		raw, err := store.Load(name)
		if err != nil || string(raw) != "{\"version\":\"2.35.10\"}" {
			t.Fatalf("%s lost after restart: %s %v", name, raw, err)
		}
	}
}
