package accountstate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestRepository(t *testing.T) (*Repository, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, path
}

func requireState(t *testing.T, r *Repository, name string, want []byte, wantGeneration int64) {
	t.Helper()
	got, generation, found, err := r.LoadContext(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !bytes.Equal(got, want) || generation != wantGeneration {
		t.Fatalf("%s: got (%q, %d, %t), want (%q, %d, true)", name, got, generation, found, want, wantGeneration)
	}
}

func TestTransactionAtomicityAndGeneration(t *testing.T) {
	r, _ := openTestRepository(t)
	ctx := context.Background()
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wallet", "deck", "progress"} {
		if generation, err := tx.Save(name, []byte(name)); err != nil || generation != 1 {
			t.Fatalf("save %s: generation %d, error %v", name, generation, err)
		}
	}
	if generation, err := tx.Save("wallet", []byte("updated")); err != nil || generation != 2 {
		t.Fatalf("second save: generation %d, error %v", generation, err)
	}
	if data, generation, found, err := tx.Load("wallet"); err != nil || !found || !bytes.Equal(data, []byte("updated")) || generation != 2 {
		t.Fatalf("read own write: %q, %d, %t, %v", data, generation, found, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := tx.Load("wallet"); !errors.Is(err, ErrClosed) {
		t.Fatalf("load after commit: %v", err)
	}
	requireState(t, r, "wallet", []byte("updated"), 2)
	requireState(t, r, "deck", []byte("deck"), 1)
	requireState(t, r, "progress", []byte("progress"), 1)
}

func TestRollbackAndReopen(t *testing.T) {
	r, path := openTestRepository(t)
	ctx := context.Background()
	if _, err := r.SaveContext(ctx, "wallet", []byte("before")); err != nil {
		t.Fatal(err)
	}
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Save("wallet", []byte("after")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Save("deck", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	requireState(t, r, "wallet", []byte("before"), 1)
	if _, _, found, err := r.LoadContext(ctx, "deck"); err != nil || found {
		t.Fatalf("rolled-back domain: found=%t err=%v", found, err)
	}
	if _, err := r.SaveContext(ctx, "wallet", []byte("committed")); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	requireState(t, reopened, "wallet", []byte("committed"), 2)
	var mode string
	if err := reopened.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %q: %v", mode, err)
	}
	var synchronous int
	if err := reopened.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("synchronous %d: %v", synchronous, err)
	}
}

func TestConcurrentTransactionsSerialize(t *testing.T) {
	r, _ := openTestRepository(t)
	first, err := r.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(entered)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		tx, err := r.Begin(ctx)
		if err != nil {
			finished <- err
			return
		}
		_, err = tx.Save("wallet", []byte("second"))
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		finished <- err
	}()
	<-entered
	select {
	case err := <-finished:
		t.Fatalf("second transaction finished before first released connection: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := first.Save("wallet", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	requireState(t, r, "wallet", []byte("second"), 2)
}

func TestOperationRoutesStoreWritesAcrossDomains(t *testing.T) {
	r, _ := openTestRepository(t)
	if err := r.Save("wallet", []byte("before")); err != nil {
		t.Fatal(err)
	}
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Save("wallet", []byte("after")); err != nil {
		t.Fatal(err)
	}
	if err := r.Save("deck", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if data, err := r.Load("wallet"); err != nil || !bytes.Equal(data, []byte("after")) {
		t.Fatalf("read active write: %q, %v", data, err)
	}
	if err := op.Rollback(); err == nil {
		t.Fatal("dirty request rollback did not require restart")
	}
	if err := r.Check(); err == nil {
		t.Fatal("repository accepted requests after dirty rollback")
	}
	if _, err := r.BeginOperation(); err == nil {
		t.Fatal("began request after dirty rollback")
	}
	// The durable rows are rolled back even though domain memory now needs reload.
	var payload []byte
	if err := r.db.QueryRow(`SELECT payload FROM domain_state WHERE name = 'wallet'`).Scan(&payload); err != nil || !bytes.Equal(payload, []byte("before")) {
		t.Fatalf("wallet after rollback: %q, %v", payload, err)
	}
	if err := r.db.QueryRow(`SELECT payload FROM domain_state WHERE name = 'deck'`).Scan(&payload); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deck after rollback: %q, %v", payload, err)
	}
}

func TestOperationCommitAndCleanRollback(t *testing.T) {
	r, _ := openTestRepository(t)
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	op, err = r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Save("wallet", []byte("committed")); err != nil {
		t.Fatal(err)
	}
	if err := r.Save("deck", []byte("committed")); err != nil {
		t.Fatal(err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	requireState(t, r, "wallet", []byte("committed"), 1)
	requireState(t, r, "deck", []byte("committed"), 1)
}

func TestSchemaVersionRejected(t *testing.T) {
	r, path := openTestRepository(t)
	if _, err := r.db.Exec(`UPDATE metadata SET value = '3' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		_ = reopened.Close()
		t.Fatal("opened unknown schema version")
	}
}

func TestPopulatedDatabaseWithoutSchemaVersionRejected(t *testing.T) {
	r, path := openTestRepository(t)
	if err := r.Save("wallet", []byte("existing")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`DELETE FROM metadata WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		_ = reopened.Close()
		t.Fatal("stamped current schema onto populated unversioned database")
	}
}

func TestCurrentVersionWithMissingTableRejected(t *testing.T) {
	r, path := openTestRepository(t)
	if _, err := r.db.Exec(`DROP TABLE domain_entry`); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		_ = reopened.Close()
		t.Fatal("recreated a missing table in an existing current-version database")
	}
}

func TestRequireDomainsRejectsPartialAccount(t *testing.T) {
	r, _ := openTestRepository(t)
	if err := r.RequireDomains("wallet"); err == nil {
		t.Fatal("accepted empty database as a complete account")
	}
	if err := r.Save("wallet", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.RequireDomains("wallet"); err != nil {
		t.Fatal(err)
	}
	if err := r.RequireDomains("wallet", "items"); err == nil {
		t.Fatal("accepted a missing account domain")
	}
}

func TestInvalidNamesAndEmptyBlob(t *testing.T) {
	r, _ := openTestRepository(t)
	ctx := context.Background()
	if _, err := r.SaveContext(ctx, "", []byte("bad")); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := r.SaveContext(ctx, "empty", nil); err != nil {
		t.Fatal(err)
	}
	requireState(t, r, "empty", []byte{}, 1)
}

func BenchmarkRequestTransaction(b *testing.B) {
	path := filepath.Join(b.TempDir(), "state.db")
	r, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	payload := bytes.Repeat([]byte("x"), 4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, err := r.Begin(ctx)
		if err != nil {
			b.Fatal(err)
		}
		for _, name := range []string{"wallet", "deck", "progress"} {
			if _, err := tx.Save(name, payload); err != nil {
				b.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}
