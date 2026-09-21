package gamedata

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"bd2server/internal/dbcrypt"
)

// DatabaseName maps a logical client DB name to its GameData archive entry.
// Version 1 is the current 2.34.13 DB schema generation.
func DatabaseName(logical string) (string, error) {
	if logical == "" || strings.ContainsAny(logical, `/\\.`) {
		return "", fmt.Errorf("gamedata: invalid logical database name %q", logical)
	}
	digest := sha1.Sum([]byte(logical + "_v1"))
	return fmt.Sprintf("%X", digest), nil
}

// ReadDatabase extracts and decrypts one SQLite database from the validated
// common-dbdata.bin. The returned bytes are an ordinary SQLite file.
func ReadDatabase(root, version, logical string) ([]byte, error) {
	name, err := DatabaseName(logical)
	if err != nil {
		return nil, err
	}
	return readEntry(root, version, name, logical)
}

// questDatabaseEntry is the member in the 2.34.13 GameData archive that
// contains the shared QuestTable* SQLite database, including QuestTable21.
// Quest data is not stored in an individual per-pack database.
const questDatabaseEntry = "9F251C63BC72551C681EE75D328FA090D56E444B"

// ReadQuestDatabase extracts the shared quest database. The member name is
// opaque in the client archive, so callers must not derive it from a map ID.
func ReadQuestDatabase(root, version string) ([]byte, error) {
	return readEntry(root, version, questDatabaseEntry, "quest tables")
}

func readEntry(root, version, name, label string) ([]byte, error) {
	archivePath := filepath.Join(root, version, "release", ArchiveName)
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("gamedata: open database archive: %w", err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if !strings.EqualFold(entry.Name, name) {
			continue
		}
		stream, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("gamedata: open database %s: %w", label, err)
		}
		encrypted, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil {
			return nil, fmt.Errorf("gamedata: read database %s: %w", label, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("gamedata: close database %s: %w", label, closeErr)
		}
		plain, err := dbcrypt.DecryptPages(encrypted)
		if err != nil {
			return nil, fmt.Errorf("gamedata: decrypt database %s: %w", label, err)
		}
		if !bytes.HasPrefix(plain, dbcrypt.Header) {
			return nil, fmt.Errorf("gamedata: database %s has no SQLite header after decryption", label)
		}
		return plain, nil
	}
	return nil, fmt.Errorf("gamedata: database %s entry %s not found", label, name)
}
