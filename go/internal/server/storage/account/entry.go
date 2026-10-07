package accountstate

import (
	"database/sql"
	"errors"
	"fmt"

	"bd2server/internal/server/storage/stateio"
)

var _ stateio.EntryStore = (*CommandStore)(nil)

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
func (t *CommandStore) LoadEntry(domain, bucket, key string) ([]byte, bool, error) {
	if err := validEntryKey(domain, bucket, key); err != nil {
		return nil, false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.available(); err != nil {
		return nil, false, err
	}
	var payload []byte
	err := t.tx.QueryRow(`SELECT payload FROM domain_entry
		WHERE domain_name = ? AND bucket = ? AND entry_key = ?`, domain, bucket, key).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		t.reject(err)
		return nil, false, fmt.Errorf("accountstate: load entry: %w", err)
	}
	return payload, true, nil
}

// ListEntries reads one domain-owned bucket from the transaction snapshot.
func (t *CommandStore) ListEntries(domain, bucket string) (map[string][]byte, error) {
	if err := validEntryScope(domain, bucket); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.available(); err != nil {
		return nil, err
	}
	rows, err := t.tx.Query(`SELECT entry_key, payload FROM domain_entry
		WHERE domain_name = ? AND bucket = ?`, domain, bucket)
	if err != nil {
		t.reject(err)
		return nil, fmt.Errorf("accountstate: list entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make(map[string][]byte)
	for rows.Next() {
		var key string
		var payload []byte
		if err := rows.Scan(&key, &payload); err != nil {
			t.reject(err)
			return nil, fmt.Errorf("accountstate: scan entry: %w", err)
		}
		entries[key] = payload
	}
	if err := rows.Err(); err != nil {
		t.reject(err)
		return nil, fmt.Errorf("accountstate: list entries: %w", err)
	}
	return entries, nil
}

// PutEntry replaces one opaque value without rewriting its domain snapshot.
func (t *CommandStore) PutEntry(domain, bucket, key string, payload []byte) error {
	if err := validEntryKey(domain, bucket, key); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.available(); err != nil {
		return err
	}
	if payload == nil {
		payload = []byte{}
	}
	result, err := t.tx.Exec(`INSERT INTO domain_entry(domain_name, bucket, entry_key, payload, generation)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(domain_name, bucket, entry_key) DO UPDATE SET
			payload = excluded.payload,
			generation = domain_entry.generation + 1
 WHERE domain_entry.payload IS NOT excluded.payload`,
		domain, bucket, key, payload)
	if err != nil {
		t.reject(err)
		return fmt.Errorf("accountstate: put entry: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		t.reject(err)
		return fmt.Errorf("accountstate: count saved entry: %w", err)
	}
	if changed != 0 {
		t.dirty = true
		t.repository.observationVersion.Add(1)
	}
	return nil
}

// DeleteEntry reports whether a record was removed.
func (t *CommandStore) DeleteEntry(domain, bucket, key string) (bool, error) {
	if err := validEntryKey(domain, bucket, key); err != nil {
		return false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.available(); err != nil {
		return false, err
	}
	result, err := t.tx.Exec(`DELETE FROM domain_entry
		WHERE domain_name = ? AND bucket = ? AND entry_key = ?`, domain, bucket, key)
	if err != nil {
		t.reject(err)
		return false, fmt.Errorf("accountstate: delete entry: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		t.reject(err)
		return false, fmt.Errorf("accountstate: count deleted entries: %w", err)
	}
	if count != 0 {
		t.dirty = true
		t.repository.observationVersion.Add(1)
	}
	return count != 0, nil
}
