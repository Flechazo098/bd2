// Package gamedata validates the versioned archive served to the client.
package gamedata

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ArchiveName = "common-dbdata.bin"
	InfoName    = "common-dbdata.info"
)

// Result describes a completely readable GameData archive. Validation reads
// every entry to EOF, which makes archive/zip verify each entry's CRC32.
type Result struct {
	ArchivePath      string
	ArchiveSize      int64
	EntryCount       int
	UncompressedSize int64
}

// Ensure returns a verified local archive. If local validation fails, it
// downloads the official size metadata and archive into staging files,
// validates the complete ZIP (including every entry CRC), and only then
// replaces the bad files. Existing files are preserved as .broken.* backups.
func Ensure(ctx context.Context, client *http.Client, root, version, origin string) (Result, bool, error) {
	if result, err := Validate(root, version); err == nil {
		return result, false, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	origin = strings.TrimRight(origin, "/")
	if origin == "" {
		return Result{}, false, fmt.Errorf("gamedata: download origin is empty")
	}
	release := filepath.Join(root, version, "release")
	if err := os.MkdirAll(release, 0o755); err != nil {
		return Result{}, false, fmt.Errorf("gamedata: create release directory: %w", err)
	}
	baseURL := origin + "/" + version + "/release/"

	infoBytes, err := downloadSmall(ctx, client, baseURL+InfoName, 1024)
	if err != nil {
		return Result{}, false, err
	}
	expected, err := parseExpectedSize(infoBytes)
	if err != nil {
		return Result{}, false, err
	}

	archiveStage, err := os.CreateTemp(release, ".common-dbdata-*.tmp")
	if err != nil {
		return Result{}, false, fmt.Errorf("gamedata: create archive staging file: %w", err)
	}
	archiveStagePath := archiveStage.Name()
	keepStage := false
	defer func() {
		_ = archiveStage.Close()
		if !keepStage {
			_ = os.Remove(archiveStagePath)
		}
	}()
	if err := downloadExact(ctx, client, baseURL+ArchiveName, archiveStage, expected); err != nil {
		return Result{}, false, err
	}
	if err := archiveStage.Sync(); err != nil {
		return Result{}, false, fmt.Errorf("gamedata: sync staged archive: %w", err)
	}
	if err := archiveStage.Close(); err != nil {
		return Result{}, false, fmt.Errorf("gamedata: close staged archive: %w", err)
	}
	verified, err := validateArchive(archiveStagePath, expected)
	if err != nil {
		return Result{}, false, fmt.Errorf("gamedata: downloaded archive failed validation: %w", err)
	}

	infoStage, err := os.CreateTemp(release, ".common-dbdata-info-*.tmp")
	if err != nil {
		return Result{}, false, fmt.Errorf("gamedata: create info staging file: %w", err)
	}
	infoStagePath := infoStage.Name()
	defer os.Remove(infoStagePath)
	if _, err := infoStage.Write(infoBytes); err != nil {
		infoStage.Close()
		return Result{}, false, fmt.Errorf("gamedata: write staged info: %w", err)
	}
	if err := infoStage.Sync(); err != nil {
		infoStage.Close()
		return Result{}, false, fmt.Errorf("gamedata: sync staged info: %w", err)
	}
	if err := infoStage.Close(); err != nil {
		return Result{}, false, fmt.Errorf("gamedata: close staged info: %w", err)
	}

	archivePath := filepath.Join(release, ArchiveName)
	infoPath := filepath.Join(release, InfoName)
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	archiveBackup, archiveMoved, err := preserveExisting(archivePath, stamp)
	if err != nil {
		return Result{}, false, err
	}
	infoBackup, infoMoved, err := preserveExisting(infoPath, stamp)
	if err != nil {
		if archiveMoved {
			_ = os.Rename(archiveBackup, archivePath)
		}
		return Result{}, false, err
	}
	rollback := func() {
		_ = os.Remove(archivePath)
		_ = os.Remove(infoPath)
		if archiveMoved {
			_ = os.Rename(archiveBackup, archivePath)
		}
		if infoMoved {
			_ = os.Rename(infoBackup, infoPath)
		}
	}
	if err := os.Rename(archiveStagePath, archivePath); err != nil {
		rollback()
		return Result{}, false, fmt.Errorf("gamedata: install verified archive: %w", err)
	}
	keepStage = true
	if err := os.Rename(infoStagePath, infoPath); err != nil {
		rollback()
		return Result{}, false, fmt.Errorf("gamedata: install size metadata: %w", err)
	}

	verified.ArchivePath = archivePath
	return verified, true, nil
}

func Validate(root, version string) (Result, error) {
	if root == "" || version == "" || strings.ContainsAny(version, `/\\`) {
		return Result{}, fmt.Errorf("gamedata: invalid root or version")
	}
	release := filepath.Join(root, version, "release")
	archivePath := filepath.Join(release, ArchiveName)
	infoPath := filepath.Join(release, InfoName)

	infoBytes, err := os.ReadFile(infoPath)
	if err != nil {
		return Result{}, fmt.Errorf("gamedata: read size metadata: %w", err)
	}
	expected, err := parseExpectedSize(infoBytes)
	if err != nil {
		return Result{}, err
	}
	stat, err := os.Stat(archivePath)
	if err != nil {
		return Result{}, fmt.Errorf("gamedata: stat archive: %w", err)
	}
	if stat.Size() != expected {
		return Result{}, fmt.Errorf("gamedata: archive size %d does not match metadata %d", stat.Size(), expected)
	}

	return validateArchive(archivePath, expected)
}

func validateArchive(archivePath string, expected int64) (Result, error) {
	stat, err := os.Stat(archivePath)
	if err != nil {
		return Result{}, fmt.Errorf("gamedata: stat archive: %w", err)
	}
	if stat.Size() != expected {
		return Result{}, fmt.Errorf("gamedata: archive size %d does not match metadata %d", stat.Size(), expected)
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return Result{}, fmt.Errorf("gamedata: open archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 {
		return Result{}, fmt.Errorf("gamedata: archive is empty")
	}

	var unpacked int64
	for index, entry := range reader.File {
		stream, err := entry.Open()
		if err != nil {
			return Result{}, fmt.Errorf("gamedata: open entry %d %q: %w", index, entry.Name, err)
		}
		read, copyErr := io.Copy(io.Discard, stream)
		closeErr := stream.Close()
		if copyErr != nil {
			return Result{}, fmt.Errorf("gamedata: verify entry %d %q: %w", index, entry.Name, copyErr)
		}
		if closeErr != nil {
			return Result{}, fmt.Errorf("gamedata: close entry %d %q: %w", index, entry.Name, closeErr)
		}
		if read != int64(entry.UncompressedSize64) {
			return Result{}, fmt.Errorf("gamedata: entry %d %q size %d, expected %d", index, entry.Name, read, entry.UncompressedSize64)
		}
		unpacked += read
	}

	return Result{
		ArchivePath:      archivePath,
		ArchiveSize:      stat.Size(),
		EntryCount:       len(reader.File),
		UncompressedSize: unpacked,
	}, nil
}

func parseExpectedSize(infoBytes []byte) (int64, error) {
	text := strings.TrimSpace(string(infoBytes))
	expected, err := strconv.ParseInt(text, 10, 64)
	if err != nil || expected <= 0 {
		return 0, fmt.Errorf("gamedata: invalid size metadata %q", text)
	}
	return expected, nil
}

func downloadSmall(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("gamedata: build metadata request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gamedata: download %s: %w", InfoName, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gamedata: download %s: HTTP %s", InfoName, response.Status)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("gamedata: read %s: %w", InfoName, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("gamedata: %s exceeds %d bytes", InfoName, limit)
	}
	return b, nil
}

func downloadExact(ctx context.Context, client *http.Client, url string, target *os.File, expected int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("gamedata: build archive request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gamedata: download %s: %w", ArchiveName, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("gamedata: download %s: HTTP %s", ArchiveName, response.Status)
	}
	written, err := io.Copy(target, io.LimitReader(response.Body, expected+1))
	if err != nil {
		return fmt.Errorf("gamedata: write staged archive: %w", err)
	}
	if written != expected {
		return fmt.Errorf("gamedata: downloaded archive size %d, expected %d", written, expected)
	}
	return nil
}

func preserveExisting(path, stamp string) (string, bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("gamedata: inspect existing file %q: %w", path, err)
	}
	backup := path + ".broken." + stamp
	if err := os.Rename(path, backup); err != nil {
		return "", false, fmt.Errorf("gamedata: preserve invalid file %q: %w", path, err)
	}
	return backup, true, nil
}
