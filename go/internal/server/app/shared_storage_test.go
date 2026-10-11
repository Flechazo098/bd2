package app

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"bd2server/internal/server/domain/command"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/outbox"
	"modernc.org/sqlite"
)

func TestOutboxDiscoveryReadsCommittedAccountState(t *testing.T) {
	discovery, path := discoveryFixture(t)
	if pending, err := discovery.HasPending(t.Context(), "missing"); err != nil || pending {
		t.Fatalf("missing account: pending=%v, err=%v", pending, err)
	}
	repository, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	})
	check := func(want bool) {
		t.Helper()
		pending, err := discovery.HasPending(t.Context(), "writer")
		if err != nil || pending != want {
			t.Fatalf("pending=%v, want %v, err=%v", pending, want, err)
		}
	}
	check(false)
	tx, err := repository.BeginCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func(tx *accountstate.CommandStore) { _ = tx.Rollback() }(tx)
	if err := outbox.Publish(tx, "writer", command.Event{ID: "committed-event", Topic: "fixture", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check(true)
	tx, err = repository.BeginCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func(tx *accountstate.CommandStore) { _ = tx.Rollback() }(tx)
	entries, err := outbox.Read(tx, 1)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read committed outbox: entries=%v, err=%v", entries, err)
	}
	if err := outbox.Acknowledge(tx, entries[0].Key, entries[0].Digest); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestOutboxDiscoveryWaitsForTransientWALLock(t *testing.T) {
	discovery, path := discoveryFixture(t)
	repository, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	})
	tx, err := repository.BeginCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func(tx *accountstate.CommandStore) { _ = tx.Rollback() }(tx)
	if err := outbox.Publish(tx, "writer", command.Event{ID: "pending-event", Topic: "fixture", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.ExecContext(t.Context(), `PRAGMA locking_mode=EXCLUSIVE; BEGIN EXCLUSIVE; UPDATE metadata SET value=value WHERE key='writer_epoch'; COMMIT`); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}
	u.RawQuery = "mode=ro"
	reader, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	var count int
	err = reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM domain_entry`).Scan(&count)
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 {
		t.Fatalf("fixture must hold a real SQLITE_BUSY WAL lock, got %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	type result struct {
		pending bool
		err     error
	}
	started := make(chan struct{})
	completed := make(chan result, 1)
	go func() {
		close(started)
		pending, err := discovery.HasPending(ctx, "writer")
		completed <- result{pending, err}
	}()
	<-started
	select {
	case result := <-completed:
		t.Fatalf("discovery returned before the transient lock cleared: pending=%v, err=%v", result.pending, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-completed:
		if result.err != nil || !result.pending {
			t.Fatalf("discovery after releasing WAL lock: pending=%v, err=%v", result.pending, result.err)
		}
	case <-ctx.Done():
		t.Fatal("discovery did not complete within its caller deadline")
	}
}

func discoveryFixture(t *testing.T) (outboxDiscovery, string) {
	t.Helper()
	root := t.TempDir()
	factory := &PlayerFactory{options: &configuration{stateDirectory: root}}
	return outboxDiscovery{factory: factory}, filepath.Join(root, "accounts", accountDirectoryName("writer"), "state.db")
}
