package instanceid_test

import (
	"context"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"bd2server/internal/common/util"
	"bd2server/internal/server/domain/command"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/instanceid"
)

func TestIndependentWritersAndRestartNeverReuseIssuedInventoryIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "instance-ids.db")
	sources := []*util.IDAllocator{instanceid.New(path), instanceid.New(path)}
	type issued struct {
		id  uint64
		err error
	}
	const workers, perWorker = 8, 257
	results := make(chan issued, workers*perWorker)
	start := make(chan struct{})
	var done sync.WaitGroup
	for worker := range workers {
		done.Go(func() {
			<-start
			for range perWorker {
				id, err := sources[worker%len(sources)].NewInstanceID()
				results <- issued{id, err}
			}
		})
	}
	close(start)
	done.Wait()
	close(results)
	seen := make(map[uint64]bool)
	remember := func(id uint64, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if id == 0 || id > math.MaxInt64 || seen[id] {
			t.Fatalf("inventory identity is invalid or reused: %d", id)
		}
		seen[id] = true
	}
	for result := range results {
		remember(result.id, result.err)
	}

	accountPath := filepath.Join(root, "player", "state.db")
	repository, err := accountstate.Open(accountPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	tx, err := repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if err := tx.PutEntry("items", "items", "uncommitted", []byte("true")); err != nil {
		t.Fatal(err)
	}
	ctx := command.Context{State: tx, InstanceIDs: sources[0]}
	remember(ctx.NewInstanceID())
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = accountstate.Open(accountPath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := tx.LoadEntry("items", "items", "uncommitted"); err != nil || found {
		t.Fatalf("account rollback was not durable: found=%t error=%v", found, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	restarted := instanceid.New(path)
	for range workers * perWorker {
		remember(restarted.NewInstanceID())
	}
	// The original sources can still finish their committed ranges after another
	// writer restarts; those IDs must also remain disjoint.
	for _, source := range sources {
		for range perWorker {
			remember(source.NewInstanceID())
		}
	}
}
