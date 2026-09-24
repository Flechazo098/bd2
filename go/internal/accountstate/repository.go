// Package accountstate stores every domain's opaque account state in one SQLite database.
package accountstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"

	"bd2server/internal/stateio"

	_ "modernc.org/sqlite"
)

const schemaVersion = 2

var ErrClosed = errors.New("accountstate: transaction already finished")

// Repository owns one SQLite connection. A request or a complete batch holds
// that connection from Begin until Commit or Rollback, serializing writers.
type Repository struct {
	db  *sql.DB
	new bool

	mu       sync.Mutex
	failed   error
	opMu     sync.Mutex
	activeMu sync.RWMutex
	active   *Tx
}

var _ stateio.TransactionalStore = (*Repository)(nil)

// Open creates or opens state.db. SQLite's WAL handles interrupted writes and
// FULL synchronous ensures a successful commit is durable before returning.
func Open(path string) (_ *Repository, err error) {
	if path == "" || filepath.Base(path) != "state.db" {
		return nil, errors.New("accountstate: path must end in state.db")
	}
	path = filepath.Clean(path)
	_, statErr := os.Stat(path)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !fresh {
		return nil, fmt.Errorf("accountstate: inspect database: %w", statErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("accountstate: create state directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("accountstate: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	ctx := context.Background()
	if err = initialize(ctx, db, fresh); err != nil {
		return nil, err
	}
	var mode string
	if err = db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return nil, fmt.Errorf("accountstate: enable WAL: %w", err)
	}
	if mode != "wal" {
		return nil, fmt.Errorf("accountstate: journal mode %q, want wal", mode)
	}
	if _, err = db.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return nil, fmt.Errorf("accountstate: enable FULL synchronous: %w", err)
	}
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return nil, fmt.Errorf("accountstate: set busy timeout: %w", err)
	}
	return &Repository{db: db, new: fresh}, nil
}

// SchemaVersion returns the on-disk version after all startup migrations.
func (r *Repository) SchemaVersion() (int, error) {
	var raw string
	if err := r.db.QueryRow(`SELECT value FROM metadata WHERE key = 'schema_version'`).Scan(&raw); err != nil {
		return 0, fmt.Errorf("accountstate: read schema version: %w", err)
	}
	version, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("accountstate: invalid schema version %q", raw)
	}
	return version, nil
}

// IsNew reports whether Open created this database during the current start.
func (r *Repository) IsNew() bool { return r.new }

// RequireDomains rejects a partial or foreign existing account database.
func (r *Repository) RequireDomains(required ...string) error {
	rows, err := r.db.Query(`SELECT name FROM domain_state ORDER BY name`)
	if err != nil {
		return fmt.Errorf("accountstate: list domains: %w", err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		found = append(found, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Strings(required)
	if !slices.Equal(found, required) {
		return fmt.Errorf("accountstate: domains %v, want %v", found, required)
	}
	return nil
}

// Begin starts a request transaction. Callers must Commit or Rollback it.
// Begin may wait until the previous transaction releases the sole connection.
func (r *Repository) Begin(ctx context.Context) (*Tx, error) {
	if err := r.Check(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("accountstate: begin transaction: %w", err)
	}
	if err := r.Check(); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return &Tx{repository: r, tx: tx}, nil
}

// Check reports uncertain commit or rollback failures. Reopen the repository
// after such an error so SQLite can finish recovery before requests resume.
func (r *Repository) Check() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed
}

func (r *Repository) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed == nil {
		r.failed = fmt.Errorf("accountstate: transaction outcome uncertain; reopen database: %w", err)
	}
}

// Close releases the SQLite connection. All transactions must be finished first.
func (r *Repository) Close() error { return r.db.Close() }

// LoadContext reads a domain outside a request transaction.
func (r *Repository) LoadContext(ctx context.Context, name string) ([]byte, int64, bool, error) {
	tx, err := r.Begin(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	defer tx.Rollback()
	return tx.Load(name)
}

// SaveContext writes a domain in its own transaction. Request handlers should
// use Tx.Save so all domains in one request or batch commit together.
func (r *Repository) SaveContext(ctx context.Context, name string, payload []byte) (int64, error) {
	tx, err := r.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	generation, err := tx.Save(name, payload)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return generation, nil
}

// Load implements stateio.Store. Within BeginOperation it reads from the
// request transaction; outside a request it performs an independent read.
func (r *Repository) Load(name string) ([]byte, error) {
	r.activeMu.RLock()
	if r.active != nil {
		data, _, _, err := r.active.Load(name)
		r.activeMu.RUnlock()
		return data, err
	}
	r.activeMu.RUnlock()
	data, _, _, err := r.LoadContext(context.Background(), name)
	return data, err
}

// Save implements stateio.Store. Every write made during BeginOperation joins
// its transaction, including writes from different domain stores in a batch.
func (r *Repository) Save(name string, payload []byte) error {
	r.activeMu.RLock()
	if r.active != nil {
		_, err := r.active.Save(name, payload)
		r.activeMu.RUnlock()
		return err
	}
	r.activeMu.RUnlock()
	_, err := r.SaveContext(context.Background(), name, payload)
	return err
}

// BeginOperation starts the session request transaction. Session dispatch
// serializes requests; opMu also keeps direct callers from overlapping them.
func (r *Repository) BeginOperation() (stateio.RequestOperation, error) {
	r.opMu.Lock()
	tx, err := r.Begin(context.Background())
	if err != nil {
		r.opMu.Unlock()
		return nil, err
	}
	r.activeMu.Lock()
	r.active = tx
	r.activeMu.Unlock()
	return &operation{repository: r, tx: tx}, nil
}

type operation struct {
	repository *Repository
	tx         *Tx
	once       sync.Once
	err        error
}

func (o *operation) finish(commit bool) error {
	o.once.Do(func() {
		o.repository.activeMu.Lock()
		if commit {
			o.err = o.tx.Commit()
		} else {
			wrote := o.tx.isDirty()
			o.err = o.tx.Rollback()
			if wrote && o.err == nil {
				o.repository.fail(errors.New("domain memory may differ after rollback"))
				o.err = o.repository.Check()
			}
		}
		o.repository.active = nil
		o.repository.activeMu.Unlock()
		o.repository.opMu.Unlock()
	})
	return o.err
}

func (o *operation) Commit() error   { return o.finish(true) }
func (o *operation) Rollback() error { return o.finish(false) }

// Tx is a SQLite transaction whose Load and Save operations share one snapshot.
type Tx struct {
	repository *Repository
	tx         *sql.Tx
	mu         sync.Mutex
	done       bool
	dirty      bool
}

func (t *Tx) isDirty() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dirty
}

// Load returns an owned copy of the payload, its generation, and whether it exists.
func (t *Tx) Load(name string) ([]byte, int64, bool, error) {
	if name == "" {
		return nil, 0, false, errors.New("accountstate: empty domain name")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil, 0, false, ErrClosed
	}
	var payload []byte
	var generation int64
	err := t.tx.QueryRow(`SELECT payload, generation FROM domain_state WHERE name = ?`, name).
		Scan(&payload, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("accountstate: load %q: %w", name, err)
	}
	return payload, generation, true, nil
}

// Save replaces one domain's opaque payload and advances its generation.
func (t *Tx) Save(name string, payload []byte) (int64, error) {
	if name == "" {
		return 0, errors.New("accountstate: empty domain name")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return 0, ErrClosed
	}
	if payload == nil {
		payload = []byte{}
	}
	var generation int64
	err := t.tx.QueryRow(`INSERT INTO domain_state(name, payload, generation)
		VALUES (?, ?, 1)
		ON CONFLICT(name) DO UPDATE SET
			payload = excluded.payload,
			generation = domain_state.generation + 1
		RETURNING generation`, name, payload).Scan(&generation)
	if err != nil {
		return 0, fmt.Errorf("accountstate: save %q: %w", name, err)
	}
	t.dirty = true
	return generation, nil
}

// Commit makes every Save in the transaction visible at once.
func (t *Tx) Commit() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return ErrClosed
	}
	t.done = true
	if err := t.tx.Commit(); err != nil {
		t.repository.fail(err)
		return fmt.Errorf("accountstate: commit transaction: %w", err)
	}
	return nil
}

// Rollback discards every Save in the transaction. It is safe to defer.
func (t *Tx) Rollback() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil
	}
	t.done = true
	if err := t.tx.Rollback(); err != nil {
		t.repository.fail(err)
		return fmt.Errorf("accountstate: rollback transaction: %w", err)
	}
	return nil
}
