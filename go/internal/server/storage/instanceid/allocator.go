package instanceid

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"bd2server/internal/common/util"
	_ "modernc.org/sqlite"
)

// Permanent IDs leave the existing temporary-character class bits clear.
const Limit uint64 = 1 << 59

// Low values are used by the client's local preview and design objects.
const First uint64 = 1 << 32

func New(path string) *util.IDAllocator {
	return util.NewIDAllocator(func(count uint64) (uint64, error) {
		return reserve(path, count)
	})
}

func reserve(path string, count uint64) (uint64, error) {
	if count == 0 || count >= Limit {
		return 0, errors.New("instance IDs: invalid range size")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;
		CREATE TABLE IF NOT EXISTS instance_sequence (id INTEGER PRIMARY KEY CHECK(id=1), next_id INTEGER NOT NULL CHECK(next_id>0))`); err != nil {
		return 0, err
	}
	if _, err = db.Exec(`INSERT OR IGNORE INTO instance_sequence(id,next_id) VALUES(1,?)`, First); err != nil {
		return 0, err
	}
	if err = os.Chmod(path, 0o600); err != nil {
		return 0, err
	}
	var first uint64
	err = db.QueryRow(`UPDATE instance_sequence SET next_id=next_id+? WHERE id=1 AND next_id<=? RETURNING next_id-?`, count, Limit-count, count).Scan(&first)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("instance IDs: global sequence exhausted")
	}
	if err != nil {
		return 0, fmt.Errorf("instance IDs: reserve committed range: %w", err)
	}
	return first, nil
}
