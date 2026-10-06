package gamedata

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"bd2server/internal/server/dbcrypt"
)

// DatabaseName maps a logical client DB name to its GameData archive entry.
// Version 1 is the current 2.35.10 DB schema generation.
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

// ReadQuestDatabase extracts the shared common database using the client's
// logical database naming rule. Quest tables are not per-pack databases.
func ReadQuestDatabase(root, version string) ([]byte, error) {
	return ReadDatabase(root, version, "common")
}

func readEntry(root, version, name, label string) ([]byte, error) {
	archivePath := filepath.Join(root, version, "release", ArchiveName)
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("gamedata: open database archive: %w", err)
	}
	defer func() { _ = reader.Close() }()
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
