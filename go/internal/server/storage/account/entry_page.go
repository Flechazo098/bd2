package accountstate

import (
	"fmt"
	"unicode/utf8"

	"bd2server/internal/server/storage/stateio"
)

func (t *CommandStore) EntryPage(domain, bucket, after string, limit int) ([]stateio.Entry, error) {
	return t.EntryPrefixPage(domain, bucket, "", after, limit)
}

func (t *CommandStore) EntryPrefixPage(domain, bucket, prefix, after string, limit int) ([]stateio.Entry, error) {
	if err := validEntryScope(domain, bucket); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 256 {
		return nil, fmt.Errorf("accountstate: entry page limit must be between 1 and 256")
	}
	if !utf8.ValidString(prefix) || !utf8.ValidString(after) {
		return nil, fmt.Errorf("accountstate: entry page cursor must be UTF-8")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.available(); err != nil {
		return nil, err
	}
	query := `SELECT entry_key,payload FROM domain_entry WHERE domain_name=? AND bucket=? AND entry_key>?`
	args := []any{domain, bucket, after}
	if prefix != "" {
		query += ` AND entry_key>=?`
		args = append(args, prefix)
		if end := prefixEnd(prefix); end != "" {
			query += ` AND entry_key<?`
			args = append(args, end)
		}
	}
	query += ` ORDER BY entry_key LIMIT ?`
	args = append(args, limit)
	rows, err := t.tx.Query(query, args...)
	if err != nil {
		t.reject(err)
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []stateio.Entry
	for rows.Next() {
		var entry stateio.Entry
		if err := rows.Scan(&entry.Key, &entry.Payload); err != nil {
			t.reject(err)
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		t.reject(err)
		return nil, err
	}
	return entries, nil
}

func prefixEnd(prefix string) string {
	runes := []rune(prefix)
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] < utf8.MaxRune {
			runes[i]++
			if runes[i] == 0xD800 {
				runes[i] = 0xE000
			}
			return string(runes[:i+1])
		}
	}
	return ""
}

func (t *CommandStore) EntryCount(domain, bucket string) (int64, error) {
	if err := validEntryScope(domain, bucket); err != nil {
		return 0, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.available(); err != nil {
		return 0, err
	}
	var count int64
	if err := t.tx.QueryRow(`SELECT COUNT(*) FROM domain_entry WHERE domain_name=? AND bucket=?`, domain, bucket).Scan(&count); err != nil {
		t.reject(err)
		return 0, err
	}
	return count, nil
}
