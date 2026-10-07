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
	"sync/atomic"

	"bd2server/internal/server/storage/stateio"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const schemaVersion = 3

var ErrCommandFailed = errors.New("accountstate: command storage operation failed")

var ErrClosed = errors.New("accountstate: transaction already finished")
var ErrFenced = stateio.ErrWriterFenced
var ErrWriterLocked = errors.New("accountstate: state database is already owned by another writer")

// Repository owns one account database and its fenced SQLite connection.
// Only a command store can consume that connection; the repository is not a
// domain store and never routes calls into an ambient transaction.
type Repository struct {
	db          *sql.DB
	new         bool
	writerEpoch int64
	writerLock  *writerLock

	mu                 sync.Mutex
	failed             error
	closeOnce          sync.Once
	closeErr           error
	observationVersion atomic.Uint64
}

// ObservationVersion invalidates projections after every write and operation
// boundary, including rollback. It never represents a durable state version.
func (r *Repository) ObservationVersion() uint64 { return r.observationVersion.Load() }

var _ stateio.CommandTransaction = (*CommandStore)(nil)

// Open creates or opens state.db. SQLite's WAL handles interrupted writes and
// FULL synchronous ensures a successful commit is durable before returning.
func Open(path string) (_ *Repository, err error) {
	if path == "" || filepath.Base(path) != "state.db" {
		return nil, errors.New("accountstate: path must end in state.db")
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("accountstate: create state directory: %w", err)
	}
	writerLock, err := acquireWriterLock(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, writerLock.release())
		}
	}()
	_, statErr := os.Stat(path)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !fresh {
		return nil, fmt.Errorf("accountstate: inspect database: %w", statErr)
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
	epoch, err := claimWriterEpoch(ctx, db)
	if err != nil {
		return nil, err
	}
	return &Repository{db: db, new: fresh, writerEpoch: epoch, writerLock: writerLock}, nil
}

func claimWriterEpoch(ctx context.Context, db *sql.DB) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("accountstate: begin writer claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='writer_epoch'`).Scan(&raw); err != nil {
		return 0, fmt.Errorf("accountstate: read writer epoch: %w", err)
	}
	current, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || current < 0 || current == int64(^uint64(0)>>1) {
		return 0, fmt.Errorf("accountstate: invalid writer epoch %q", raw)
	}
	next := current + 1
	if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value=? WHERE key='writer_epoch'`, strconv.FormatInt(next, 10)); err != nil {
		return 0, fmt.Errorf("accountstate: advance writer epoch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("accountstate: commit writer claim: %w", err)
	}
	return next, nil
}

// SchemaVersion returns the strictly validated current on-disk version.
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
	defer func() { _ = rows.Close() }()
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

// BeginCommand binds all domain IO to one command snapshot. The owning actor
// must keep this store private, pass it explicitly to its owned services, and
// finish it before publishing a reply or accepting the next command.
func (r *Repository) BeginCommand(ctx context.Context) (*CommandStore, error) {
	if err := r.Check(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		r.storageError(err)
		return nil, fmt.Errorf("accountstate: begin transaction: %w", err)
	}
	var rawEpoch string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='writer_epoch'`).Scan(&rawEpoch); err != nil {
		r.storageError(err)
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			r.fail(rollbackErr)
		}
		return nil, fmt.Errorf("accountstate: verify writer epoch: %w", err)
	}
	epoch, parseErr := strconv.ParseInt(rawEpoch, 10, 64)
	if parseErr != nil || epoch != r.writerEpoch {
		_ = tx.Rollback()
		err := fmt.Errorf("%w: process=%d database=%q", ErrFenced, r.writerEpoch, rawEpoch)
		r.fail(err)
		return nil, r.Check()
	}
	if err := r.Check(); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	r.observationVersion.Add(1)
	return &CommandStore{repository: r, tx: tx}, nil
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
		r.failed = fmt.Errorf("%w: accountstate transaction outcome uncertain; reopen database: %w", stateio.ErrStateRecoveryRequired, err)
	}
}

// Close releases the SQLite connection and then the cross-process writer lock.
// All transactions must be finished first.
func (r *Repository) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = errors.Join(r.db.Close(), r.writerLock.release())
	})
	return r.closeErr
}

// CommandStore is a capability bound permanently to one command transaction.
// It cannot be rebound or reused; references retained past its command fail.
type CommandStore struct {
	repository   *Repository
	tx           *sql.Tx
	mu           sync.Mutex
	done         bool
	dirty        bool
	statementErr error
}

func (t *CommandStore) Dirty() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dirty
}

// Load returns an owned payload from this command snapshot.
func (t *CommandStore) Load(name string) ([]byte, error) {
	if name == "" {
		return nil, errors.New("accountstate: empty domain name")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.available(); err != nil {
		return nil, err
	}
	var payload []byte
	err := t.tx.QueryRow(`SELECT payload FROM domain_state WHERE name = ?`, name).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		t.reject(err)
		return nil, fmt.Errorf("accountstate: load %q: %w", name, err)
	}
	return payload, nil
}

// Save replaces a changed domain payload and advances its storage generation.
func (t *CommandStore) Save(name string, payload []byte) error {
	if name == "" {
		return errors.New("accountstate: empty domain name")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.available(); err != nil {
		return err
	}
	if payload == nil {
		payload = []byte{}
	}
	result, err := t.tx.Exec(`INSERT INTO domain_state(name, payload, generation)
		VALUES (?, ?, 1)
		ON CONFLICT(name) DO UPDATE SET
			payload = excluded.payload,
			generation = domain_state.generation + 1
 WHERE domain_state.payload IS NOT excluded.payload`, name, payload)
	if err != nil {
		t.reject(err)
		return fmt.Errorf("accountstate: save %q: %w", name, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		t.reject(err)
		return fmt.Errorf("accountstate: count saved domain: %w", err)
	}
	if changed != 0 {
		t.dirty = true
		t.repository.observationVersion.Add(1)
	}
	return nil
}

// Commit makes every Save in the transaction visible at once.
func (t *CommandStore) Commit() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return ErrClosed
	}
	t.done = true
	t.repository.observationVersion.Add(1)
	if t.statementErr != nil {
		rollbackErr := t.tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.repository.fail(rollbackErr)
		}
		return errors.Join(t.statementErr, t.repository.Check())
	}
	if err := t.tx.Commit(); err != nil {
		t.repository.fail(err)
		return errors.Join(fmt.Errorf("accountstate: commit transaction: %w", err), t.repository.Check())
	}
	return nil
}

// Rollback discards every Save in the transaction. It is safe to defer.
func (t *CommandStore) Rollback() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil
	}
	t.done = true
	t.repository.observationVersion.Add(1)
	if err := t.tx.Rollback(); err != nil {
		t.repository.fail(err)
		return errors.Join(fmt.Errorf("accountstate: rollback transaction: %w", err), t.repository.Check())
	}
	return nil
}

func (t *CommandStore) ObservationVersion() uint64 { return t.repository.ObservationVersion() }

func (r *Repository) storageError(err error) {
	var sqliteError *sqlite.Error
	if !errors.As(err, &sqliteError) {
		return
	}
	switch sqliteError.Code() & 0xff {
	case sqlite3.SQLITE_IOERR, sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB, sqlite3.SQLITE_FULL, sqlite3.SQLITE_CANTOPEN, sqlite3.SQLITE_READONLY:
		r.fail(err)
	}
}

func (t *CommandStore) available() error {
	if t.done {
		return ErrClosed
	}
	if t.statementErr != nil {
		return t.statementErr
	}
	return t.repository.Check()
}
func (t *CommandStore) reject(err error) {
	t.dirty = true
	t.repository.storageError(err)
	if t.statementErr == nil {
		t.statementErr = errors.Join(ErrCommandFailed, err)
	}
}
