package gamedata

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/dbcrypt"
)

// These tests cover the production archive/decryption contract. Interactive
// queries against the installed 178 MB GameData belong in tools/gamedata_db.py
// so ordinary Go tests never double as an ad-hoc database console.
func TestReadDatabase(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "123", "release")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, dbcrypt.PageSize)
	copy(plain, dbcrypt.Header)
	copy(plain[100:], []byte("CREATE TABLE QuestTable21"))
	encrypted, err := dbcrypt.EncryptPages(plain)
	if err != nil {
		t.Fatal(err)
	}
	name, err := DatabaseName("pack21")
	if err != nil {
		t.Fatal(err)
	}
	writeDatabaseTestArchive(t, filepath.Join(release, ArchiveName), name, encrypted)
	got, err := ReadDatabase(root, "123", "pack21")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("decrypted database mismatch")
	}
}

func TestReadQuestDatabase(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "123", "release")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, dbcrypt.PageSize)
	copy(plain, dbcrypt.Header)
	copy(plain[100:], []byte("CREATE TABLE QuestTable21"))
	encrypted, err := dbcrypt.EncryptPages(plain)
	if err != nil {
		t.Fatal(err)
	}
	writeDatabaseTestArchive(t, filepath.Join(release, ArchiveName), questDatabaseEntry, encrypted)
	got, err := ReadQuestDatabase(root, "123")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("decrypted quest database mismatch")
	}
}

func writeDatabaseTestArchive(t *testing.T, path, member string, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create(member)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	if _, err := entry.Write(content); err != nil {
		archive.Close()
		file.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
