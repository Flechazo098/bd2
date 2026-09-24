package accountstate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestEntriesShareRequestTransaction(t *testing.T) {
	r, _ := openTestRepository(t)
	if _, err := r.SaveContext(context.Background(), "collection", []byte("unchanged")); err != nil {
		t.Fatal(err)
	}
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PutEntry("collection", "grants", "draw:1", []byte(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.PutEntry("collection", "applied", "draw:1", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Save("wallet", []byte("charged")); err != nil {
		t.Fatal(err)
	}
	if got, found, err := r.LoadEntry("collection", "grants", "draw:1"); err != nil || !found || !bytes.Equal(got, []byte(`{"id":1}`)) {
		t.Fatalf("read own entry write: %q, %t, %v", got, found, err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	requireState(t, r, "collection", []byte("unchanged"), 1)
	requireState(t, r, "wallet", []byte("charged"), 1)
	entries, err := r.ListEntries("collection", "grants")
	if err != nil || len(entries) != 1 || !bytes.Equal(entries["draw:1"], []byte(`{"id":1}`)) {
		t.Fatalf("grant entries: %#v, %v", entries, err)
	}
	if got, found, err := r.LoadEntry("collection", "applied", "draw:1"); err != nil || !found || len(got) != 0 {
		t.Fatalf("empty marker: %q, %t, %v", got, found, err)
	}
}

func TestEntryRollbackAndDelete(t *testing.T) {
	r, _ := openTestRepository(t)
	if err := r.PutEntry("collection", "grants", "draw:1", []byte("before")); err != nil {
		t.Fatal(err)
	}
	tx, err := r.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.PutEntry("collection", "grants", "draw:1", []byte("after")); err != nil {
		t.Fatal(err)
	}
	if err := tx.PutEntry("collection", "grants", "draw:2", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, found, err := r.LoadEntry("collection", "grants", "draw:1"); err != nil || !found || !bytes.Equal(got, []byte("before")) {
		t.Fatalf("rolled-back replacement: %q, %t, %v", got, found, err)
	}
	if _, found, err := r.LoadEntry("collection", "grants", "draw:2"); err != nil || found {
		t.Fatalf("rolled-back insertion: %t, %v", found, err)
	}
	if deleted, err := r.DeleteEntry("collection", "grants", "draw:1"); err != nil || !deleted {
		t.Fatalf("delete existing entry: %t, %v", deleted, err)
	}
	if deleted, err := r.DeleteEntry("collection", "grants", "draw:1"); err != nil || deleted {
		t.Fatalf("delete missing entry: %t, %v", deleted, err)
	}
	if _, found, err := r.LoadEntry("collection", "grants", "draw:1"); err != nil || found {
		t.Fatalf("deleted entry: %t, %v", found, err)
	}
}

func TestEntryWriteMarksRequestRollbackDirty(t *testing.T) {
	r, _ := openTestRepository(t)
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PutEntry("collection", "grants", "draw:1", []byte("pending")); err != nil {
		t.Fatal(err)
	}
	if err := op.Rollback(); err == nil {
		t.Fatal("dirty entry rollback did not require restart")
	}
	if err := r.Check(); err == nil {
		t.Fatal("repository accepted requests after dirty entry rollback")
	}
	var count int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM domain_entry`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("entry survived rollback: count %d, error %v", count, err)
	}
}

func TestEntryScopeAndClosedTransaction(t *testing.T) {
	r, _ := openTestRepository(t)
	if err := r.PutEntry("", "grants", "key", nil); err == nil {
		t.Fatal("accepted empty domain")
	}
	if err := r.PutEntry("collection", "", "key", nil); err == nil {
		t.Fatal("accepted empty bucket")
	}
	if err := r.PutEntry("collection", "grants", "", nil); err == nil {
		t.Fatal("accepted empty key")
	}
	tx, err := r.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx.PutEntry("collection", "grants", "key", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("put after commit: %v", err)
	}
	if _, err := tx.ListEntries("collection", "grants"); !errors.Is(err, ErrClosed) {
		t.Fatalf("list after commit: %v", err)
	}
}

func TestOldSchemaIsRejectedWithoutMutation(t *testing.T) {
	r, path := openTestRepository(t)
	if _, err := r.SaveContext(context.Background(), "collection", []byte("legacy snapshot")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`DROP TABLE domain_entry`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`UPDATE metadata SET value = '0' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		_ = reopened.Close()
		t.Fatal("opened unsupported schema version")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow(`SELECT value FROM metadata WHERE key = 'schema_version'`).Scan(&version); err != nil || version != "0" {
		t.Fatalf("changed unsupported version %q: %v", version, err)
	}
	var entryTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'domain_entry'`).Scan(&entryTables); err != nil || entryTables != 0 {
		t.Fatalf("created table during failed open: %d, %v", entryTables, err)
	}
}
