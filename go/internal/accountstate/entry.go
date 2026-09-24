package accountstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bd2server/internal/stateio"
)

var _ stateio.EntryStore = (*Repository)(nil)

func validEntryScope(domain, bucket string) error {
	if domain == "" || bucket == "" {
		return errors.New("accountstate: empty entry domain or bucket")
	}
	return nil
}

func validEntryKey(domain, bucket, key string) error {
	if err := validEntryScope(domain, bucket); err != nil {
		return err
	}
	if key == "" {
		return errors.New("accountstate: empty entry key")
	}
	return nil
}

// LoadEntry distinguishes a missing key from an existing empty payload.
func (t *Tx) LoadEntry(domain, bucket, key string) ([]byte, bool, error) {
	if err := validEntryKey(domain, bucket, key); err != nil {
		return nil, false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil, false, ErrClosed
	}
	var payload []byte
	err := t.tx.QueryRow(`SELECT payload FROM domain_entry
		WHERE domain_name = ? AND bucket = ? AND entry_key = ?`, domain, bucket, key).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("accountstate: load entry: %w", err)
	}
	return payload, true, nil
}

// ListEntries reads one domain-owned bucket from the transaction snapshot.
func (t *Tx) ListEntries(domain, bucket string) (map[string][]byte, error) {
	if err := validEntryScope(domain, bucket); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil, ErrClosed
	}
	rows, err := t.tx.Query(`SELECT entry_key, payload FROM domain_entry
		WHERE domain_name = ? AND bucket = ?`, domain, bucket)
	if err != nil {
		return nil, fmt.Errorf("accountstate: list entries: %w", err)
	}
	defer rows.Close()
	entries := make(map[string][]byte)
	for rows.Next() {
		var key string
		var payload []byte
		if err := rows.Scan(&key, &payload); err != nil {
			return nil, fmt.Errorf("accountstate: scan entry: %w", err)
		}
		entries[key] = payload
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("accountstate: list entries: %w", err)
	}
	return entries, nil
}

// PutEntry replaces one opaque value without rewriting its domain snapshot.
func (t *Tx) PutEntry(domain, bucket, key string, payload []byte) error {
	if err := validEntryKey(domain, bucket, key); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return ErrClosed
	}
	if payload == nil {
		payload = []byte{}
	}
	_, err := t.tx.Exec(`INSERT INTO domain_entry(domain_name, bucket, entry_key, payload, generation)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(domain_name, bucket, entry_key) DO UPDATE SET
			payload = excluded.payload,
			generation = domain_entry.generation + 1`,
		domain, bucket, key, payload)
	if err != nil {
		return fmt.Errorf("accountstate: put entry: %w", err)
	}
	t.dirty = true
	return nil
}

// DeleteEntry reports whether a record was removed.
func (t *Tx) DeleteEntry(domain, bucket, key string) (bool, error) {
	if err := validEntryKey(domain, bucket, key); err != nil {
		return false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return false, ErrClosed
	}
	result, err := t.tx.Exec(`DELETE FROM domain_entry
		WHERE domain_name = ? AND bucket = ? AND entry_key = ?`, domain, bucket, key)
	if err != nil {
		return false, fmt.Errorf("accountstate: delete entry: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("accountstate: count deleted entries: %w", err)
	}
	if count != 0 {
		t.dirty = true
	}
	return count != 0, nil
}

func (r *Repository) LoadEntry(domain, bucket, key string) ([]byte, bool, error) {
	r.activeMu.RLock()
	if r.active != nil {
		payload, found, err := r.active.LoadEntry(domain, bucket, key)
		r.activeMu.RUnlock()
		return payload, found, err
	}
	r.activeMu.RUnlock()
	tx, err := r.Begin(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	return tx.LoadEntry(domain, bucket, key)
}

func (r *Repository) ListEntries(domain, bucket string) (map[string][]byte, error) {
	r.activeMu.RLock()
	if r.active != nil {
		entries, err := r.active.ListEntries(domain, bucket)
		r.activeMu.RUnlock()
		return entries, err
	}
	r.activeMu.RUnlock()
	tx, err := r.Begin(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return tx.ListEntries(domain, bucket)
}

func (r *Repository) PutEntry(domain, bucket, key string, payload []byte) error {
	r.activeMu.RLock()
	if r.active != nil {
		err := r.active.PutEntry(domain, bucket, key, payload)
		r.activeMu.RUnlock()
		return err
	}
	r.activeMu.RUnlock()
	tx, err := r.Begin(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.PutEntry(domain, bucket, key, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) DeleteEntry(domain, bucket, key string) (bool, error) {
	r.activeMu.RLock()
	if r.active != nil {
		deleted, err := r.active.DeleteEntry(domain, bucket, key)
		r.activeMu.RUnlock()
		return deleted, err
	}
	r.activeMu.RUnlock()
	tx, err := r.Begin(context.Background())
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	deleted, err := tx.DeleteEntry(domain, bucket, key)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return deleted, nil
}
