package download

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"

	commonconfig "bd2server/internal/common/config"
)

const gameReceiptName = ".bd2-game-install.json"

type installedFile struct {
	SourceBytes uint64  `json:"source_bytes"`
	SourceCRC32 uint32  `json:"source_crc32"`
	Verified    receipt `json:"verified"`
}
type gameInstallation struct {
	SchemaVersion int                      `json:"schema_version"`
	ArchiveSHA256 string                   `json:"archive_sha256"`
	Files         map[string]installedFile `json:"files"`
}

func readInstallation(root string) (gameInstallation, error) {
	return commonconfig.Load(filepath.Join(root, gameReceiptName), gameInstallation{}, commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true, RejectNull: true})
}

func fileHashes(path string) (receipt, uint32, error) {
	in, err := os.Open(path)
	if err != nil {
		return receipt{}, 0, err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return receipt{}, 0, err
	}
	if !info.Mode().IsRegular() {
		return receipt{}, 0, errors.New("installed game file is not regular")
	}
	sha, crc := sha256.New(), crc32.NewIEEE()
	n, err := io.Copy(io.MultiWriter(sha, crc), in)
	if err != nil {
		return receipt{}, 0, err
	}
	after, err := os.Stat(path)
	if err != nil {
		return receipt{}, 0, err
	}
	if n != info.Size() || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		return receipt{}, 0, errors.New("installed game file changed during verification")
	}
	return receipt{Bytes: n, Modified: after.ModTime().UnixNano(), SHA256: hex.EncodeToString(sha.Sum(nil))}, crc.Sum32(), nil
}

func installedMatches(entry archiveEntry, previous installedFile) (installedFile, bool, error) {
	info, err := os.Stat(entry.target)
	if errors.Is(err, os.ErrNotExist) {
		return installedFile{}, false, nil
	}
	if err != nil {
		return installedFile{}, false, err
	}
	if !info.Mode().IsRegular() {
		return installedFile{}, false, fmt.Errorf("installed game output is not a regular file: %s", entry.target)
	}
	bound := previous.SourceBytes == entry.file.UncompressedSize64 && previous.SourceCRC32 == entry.file.CRC32 && sha256Pattern.MatchString(previous.Verified.SHA256)
	if bound && previous.Verified.Bytes == info.Size() && previous.Verified.Modified == info.ModTime().UnixNano() {
		return previous, true, nil
	}
	verified, crc, err := fileHashes(entry.target)
	if err != nil {
		return installedFile{}, false, err
	}
	if bound && verified.SHA256 == previous.Verified.SHA256 || uint64(verified.Bytes) == entry.file.UncompressedSize64 && crc == entry.file.CRC32 {
		return installedFile{SourceBytes: entry.file.UncompressedSize64, SourceCRC32: entry.file.CRC32, Verified: verified}, true, nil
	}
	return installedFile{}, false, nil
}

func extractGame(entries []archiveEntry, root, digest string) error {
	previous, err := readInstallation(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if previous.SchemaVersion != 1 || previous.ArchiveSHA256 != digest {
		previous = gameInstallation{}
	}
	next := gameInstallation{SchemaVersion: 1, ArchiveSHA256: digest, Files: map[string]installedFile{}}
	changed := previous.Files == nil
	for _, entry := range entries {
		if entry.file.FileInfo().IsDir() {
			if err := os.MkdirAll(entry.target, 0755); err != nil {
				return err
			}
			continue
		}
		relative, err := filepath.Rel(root, entry.target)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		verified, ready, err := installedMatches(entry, previous.Files[relative])
		if err != nil {
			return err
		}
		if !ready {
			if err := extractEntry(entry); err != nil {
				return fmt.Errorf("extract %s: %w", entry.file.Name, err)
			}
			verified, ready, err = installedMatches(entry, installedFile{})
			if err != nil {
				return err
			}
			if !ready {
				return fmt.Errorf("extracted game file failed verification: %s", relative)
			}
		}
		next.Files[relative] = verified
		if previous.Files[relative] != verified {
			changed = true
		}
	}
	if len(previous.Files) != len(next.Files) {
		changed = true
	}
	if !changed {
		return nil
	}
	return commonconfig.Save(filepath.Join(root, gameReceiptName), next, commonconfig.WriteOptions{FileMode: 0644})
}

func RecordPreparedGame(gameDir string, relativePaths ...string) error {
	for _, relative := range relativePaths {
		if !slices.Contains([]string{"BrownDust II_Data/resources.assets", "BrownDust II_Data/StreamingAssets/aa/catalog.json"}, relative) {
			return fmt.Errorf("cannot register unrecognized prepared game file: %s", relative)
		}
	}
	record, err := readInstallation(gameDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.SchemaVersion != 1 || record.Files == nil {
		return errors.New("unsupported game installation receipt")
	}
	changed := false
	for _, relative := range relativePaths {
		entry, ok := record.Files[relative]
		if !ok {
			return fmt.Errorf("prepared game file is absent from official archive: %s", relative)
		}
		path, err := safeTarget(gameDir, relative)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("prepared game output is not regular: %s", relative)
		}
		if info.Size() == entry.Verified.Bytes && info.ModTime().UnixNano() == entry.Verified.Modified {
			continue
		}
		entry.Verified, _, err = fileHashes(path)
		if err != nil {
			return err
		}
		record.Files[relative] = entry
		changed = true
	}
	if !changed {
		return nil
	}
	return commonconfig.Save(filepath.Join(gameDir, gameReceiptName), record, commonconfig.WriteOptions{FileMode: 0644})
}
