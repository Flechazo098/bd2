package auth

import (
	"bytes"
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
