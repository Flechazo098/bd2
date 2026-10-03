package auth

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestStoreRequiresAndClearsExactMasterKey(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "short.db"), make([]byte, 31)); err == nil {
		t.Fatal("store accepted a non-256-bit master key")
	}
	key := bytes.Repeat([]byte{0x7a}, 32)
	store, err := Open(filepath.Join(t.TempDir(), "auth.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index, value := range key {
		if value != 0 {
			t.Fatalf("master key byte %d was retained by the caller buffer", index)
		}
	}
}

func TestStoreMigratesSchemaV1ToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	store, err := Open(path, bytes.Repeat([]byte{0x61}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE refresh_attempts; UPDATE metadata SET value='1' WHERE key='schema_version'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, bytes.Repeat([]byte{0x61}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var version int
	if err := reopened.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM metadata WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema_version=%d, want 2", version)
	}
	var table string
	if err := reopened.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='refresh_attempts'`).Scan(&table); err != nil {
		t.Fatal(err)
	}
}

func TestStorePersistsExplicitSchemaVersionAndRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	store, err := Open(path, bytes.Repeat([]byte{0x35}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := store.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM metadata WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema_version=%d, want %d", version, schemaVersion)
	}
	if _, err := store.db.Exec(`UPDATE metadata SET value='999' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, bytes.Repeat([]byte{0x35}, 32)); err == nil {
		t.Fatal("store accepted an unknown schema version")
	}
}
