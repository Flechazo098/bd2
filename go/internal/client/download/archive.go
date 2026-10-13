package download

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var requiredGameFiles = []string{"BrownDust II.exe", "UnityPlayer.dll", "BrownDust II_Data/globalgamemanagers", "BrownDust II_Data/Managed/Assembly-CSharp.dll"}

func InstallBepInEx(packageDir, gameDir string) error {
	gameDir, err := filepath.Abs(gameDir)
	if err != nil {
		return err
	}
	if err := requireStopped(gameDir); err != nil {
		return err
	}
	return extractArchive(filepath.Join(packageDir, "bepinex.zip"), gameDir, false, "")
}

func safeTarget(root, relative string) (string, error) {
	relative = strings.ReplaceAll(relative, "\\", "/")
	for part := range strings.SplitSeq(relative, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, ":\x00") || strings.TrimRight(part, " .") != part {
			return "", fmt.Errorf("unsafe relative path %q", relative)
		}
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	for current := target; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink in output path %s", current)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if current == root {
			break
		}
		if filepath.Dir(current) == current {
			return "", errors.New("path outside output root")
		}
	}
	return target, nil
}

func validBundle(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return false
	}
	header = header[:n]
	if !bytes.HasPrefix(header, []byte("UnityFS\x00")) || len(header) < 12 {
		return false
	}
	offset := 12
	for range 2 {
		end := bytes.IndexByte(header[offset:], 0)
		if end < 0 {
			return false
		}
		offset += end + 1
	}
	return len(header) >= offset+8 && binary.BigEndian.Uint64(header[offset:offset+8]) == uint64(size)
}

type archiveEntry struct {
	file   *zip.File
	target string
}

func archiveEntries(archive *zip.ReadCloser, root, prefix string) ([]archiveEntry, error) {
	var entries []archiveEntry
	seen := map[string]bool{}
	for _, entry := range archive.File {
		name := strings.ReplaceAll(entry.Name, "\\", "/")
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		relative := strings.TrimSuffix(strings.TrimPrefix(name, prefix), "/")
		if relative == "" {
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("ZIP symlink %s is forbidden", name)
		}
		target, err := safeTarget(root, relative)
		if err != nil {
			return nil, err
		}
		if seen[strings.ToLower(relative)] {
			return nil, fmt.Errorf("duplicate ZIP output %s", relative)
		}
		seen[strings.ToLower(relative)] = true
		entries = append(entries, archiveEntry{entry, target})
	}
	return entries, nil
}

func extractEntry(entry archiveEntry) error {
	if entry.file.FileInfo().IsDir() {
		return os.MkdirAll(entry.target, 0755)
	}
	if err := os.MkdirAll(filepath.Dir(entry.target), 0755); err != nil {
		return err
	}
	in, err := entry.file.Open()
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.CreateTemp(filepath.Dir(entry.target), ".bd2-extract-*.part")
	if err != nil {
		return err
	}
	defer func() { _ = out.Close(); _ = os.Remove(out.Name()) }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), entry.target)
}

func extractArchive(path, root string, game bool, digest string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	prefix := ""
	if game {
		matches := 0
		for _, entry := range archive.File {
			name := strings.ReplaceAll(entry.Name, "\\", "/")
			if filepath.Base(name) == "BrownDust II.exe" {
				matches++
				prefix = strings.TrimSuffix(name, "BrownDust II.exe")
			}
		}
		if matches != 1 {
			return errors.New("game ZIP must contain exactly one BrownDust II.exe")
		}
	}
	entries, err := archiveEntries(archive, root, prefix)
	if err != nil {
		return err
	}
	required := requiredGameFiles
	if !game {
		required = []string{"winhttp.dll", "doorstop_config.ini", "BepInEx/core/BepInEx.dll"}
	}
	present := map[string]bool{}
	for _, entry := range entries {
		relative, err := filepath.Rel(root, entry.target)
		if err != nil {
			return err
		}
		if !entry.file.FileInfo().IsDir() {
			present[filepath.ToSlash(relative)] = true
		}
	}
	for _, name := range required {
		if !present[name] {
			return fmt.Errorf("ZIP missing %s", name)
		}
	}
	if !game {
		core, err := safeTarget(root, "BepInEx/core")
		if err != nil {
			return err
		}
		if err := os.RemoveAll(core); err != nil {
			return err
		}
	}
	marker := filepath.Join(root, ".bd2-official-game-version")
	if game {
		value, err := os.ReadFile(marker)
		ready := err == nil && strings.TrimSpace(string(value)) == digest
		for _, entry := range entries {
			info, err := os.Stat(entry.target)
			if err != nil || info.IsDir() != entry.file.FileInfo().IsDir() || (!info.IsDir() && uint64(info.Size()) != entry.file.UncompressedSize64) {
				ready = false
				break
			}
		}
		if ready {
			return nil
		}
	}
	for _, entry := range entries {
		if err := extractEntry(entry); err != nil {
			return fmt.Errorf("extract %s: %w", entry.file.Name, err)
		}
	}
	if game {
		return atomicWrite(marker, []byte(digest+"\n"))
	}
	return nil
}

func validateDataArchive(path string) (int, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("GameData ZIP: %w", err)
	}
	defer func() { _ = archive.Close() }()
	if len(archive.File) == 0 {
		return 0, errors.New("empty GameData ZIP")
	}
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return 0, err
		}
		_, readErr := io.Copy(io.Discard, reader)
		if err := errors.Join(readErr, reader.Close()); err != nil {
			return 0, fmt.Errorf("GameData ZIP entry %s: %w", entry.Name, err)
		}
	}
	return len(archive.File), nil
}
