package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"bd2server/internal/client/download"
	clientlayout "bd2server/internal/client/layout"
	"bd2server/internal/common/resourcecatalog"
	"bd2server/internal/common/util"
)

const (
	localCatalogPrefix  = "{UnityEngine.AddressableAssets.Addressables.RuntimePath}\\"
	localCatalogBackup  = "catalog.json.bd2-before-local-sync"
	localBundleManifest = ".bd2-local-bundles.json"
	maximumCatalogBytes = 128 << 20
)

func InstallLocalCatalog(gameDir, localRoot, bundleVersion string) error {
	installation, err := clientlayout.Resolve(gameDir)
	if err != nil {
		return err
	}
	release := filepath.Join(filepath.Clean(localRoot), "ServerData", "StandaloneWindows64", "HD", bundleVersion)
	aa := filepath.Join(installation.Data, "StreamingAssets", "aa")
	cache, err := addressablesCacheDirectory(installation)
	if err != nil {
		return err
	}
	if err := separateCatalogDirectories(release, aa, cache); err != nil {
		return err
	}
	for _, entry := range []struct{ root, relative string }{
		{release, "catalog_alpha.json"}, {release, "catalog_alpha.hash"},
		{aa, "catalog.json"}, {aa, localCatalogBackup}, {aa, localBundleManifest}, {aa, localCatalogReceipt},
		{cache, "catalog_alpha.json"}, {cache, "catalog_alpha.hash"},
	} {
		if _, err := catalogFilePath(entry.root, entry.relative); err != nil {
			return err
		}
	}
	managed, err := managedCatalogBundles(aa)
	if err != nil {
		return err
	}
	for relative := range managed {
		for _, root := range []string{release, aa} {
			if _, err := catalogFilePath(root, relative); err != nil {
				return err
			}
		}
	}
	if localCatalogCurrent(release, aa, cache) {
		return nil
	}
	source := filepath.Join(release, "catalog_alpha.json")
	sourceHash := filepath.Join(release, "catalog_alpha.hash")
	raw, err := readCatalog(source)
	if err != nil {
		return err
	}
	paths, err := resourcecatalog.BundlePaths(raw)
	if err != nil {
		return err
	}
	target := filepath.Join(aa, "catalog.json")
	current, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read built-in Addressables catalog: %w", err)
	}
	hash, err := os.ReadFile(sourceHash)
	if err != nil {
		return fmt.Errorf("read local ServerData catalog hash: %w", err)
	}
	if len(hash) == 0 || len(hash) > 1024 {
		return errors.New("local ServerData catalog hash has an invalid size")
	}
	raw, err = localizeCatalog(raw, release, aa, managed)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, raw) {
		backup := filepath.Join(aa, localCatalogBackup)
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			if err := writeExclusiveFile(backup, current, 0o600); err != nil {
				return fmt.Errorf("back up built-in Addressables catalog: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("inspect built-in Addressables catalog backup: %w", err)
		}
		if err := replaceCatalog(target, raw); err != nil {
			return fmt.Errorf("install current local Addressables catalog: %w", err)
		}
	}
	var records []managedBundle
	for _, relative := range paths {
		sourceInfo, err := os.Stat(filepath.Join(release, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		targetInfo, err := os.Stat(filepath.Join(aa, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		records = append(records, managedBundle{Path: relative, SourceSize: sourceInfo.Size(), SourceModified: sourceInfo.ModTime().UnixNano(), TargetSize: targetInfo.Size(), TargetModified: targetInfo.ModTime().UnixNano()})
	}
	managedRaw, err := json.Marshal(records)
	if err != nil {
		return err
	}
	if err := synchronizePersistentCatalog(cache, raw, hash); err != nil {
		return err
	}
	if installation.Kind == clientlayout.Windows {
		if err := download.RecordPreparedGame(gameDir, "BrownDust II_Data/StreamingAssets/aa/catalog.json"); err != nil {
			return err
		}
	}
	if err := replaceCatalog(filepath.Join(aa, localBundleManifest), managedRaw); err != nil {
		return fmt.Errorf("record managed Addressables bundles: %w", err)
	}
	return recordLocalCatalog(release, aa, cache, raw)
}

func addressablesCacheDirectory(installation clientlayout.Installation) (string, error) {
	gameRoot := installation.Root
	if installation.Kind == clientlayout.MacOS {
		gameRoot = filepath.Dir(gameRoot)
	}
	instanceRoot := filepath.Dir(gameRoot)
	if filepath.Base(gameRoot) == "game" {
		marker, err := os.ReadFile(filepath.Join(instanceRoot, "instance.json"))
		if err == nil {
			var owner struct {
				SchemaVersion int    `json:"schema_version"`
				ID            string `json:"id"`
			}
			if err := json.Unmarshal(marker, &owner); err != nil || owner.SchemaVersion != 1 || owner.ID != filepath.Base(instanceRoot) {
				return "", errors.New("invalid client instance manifest for Addressables cache")
			}
			return filepath.Join(instanceRoot, "unity", "persistent", "com.unity.addressables"), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read client instance manifest: %w", err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate user home for Addressables cache: %w", err)
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "LocalLow", "Gamfs", "BrownDust II", "com.unity.addressables"), nil
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Gamfs", "BrownDust II", "com.unity.addressables"), nil
	default:
		return "", fmt.Errorf("local Brown Dust II resources are unsupported on %s", runtime.GOOS)
	}
}

func synchronizePersistentCatalog(cache string, catalog, hash []byte) error {
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return fmt.Errorf("create persistent Addressables cache: %w", err)
	}
	for _, file := range []struct {
		name string
		data []byte
	}{
		{name: "catalog_alpha.json", data: catalog},
		{name: "catalog_alpha.hash", data: hash},
	} {
		target := filepath.Join(cache, file.name)
		current, err := os.ReadFile(target)
		if err == nil && bytes.Equal(current, file.data) {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read persistent Addressables %s: %w", file.name, err)
		}
		if err == nil {
			backup := target + ".bd2-before-local-sync"
			if _, backupErr := os.Stat(backup); errors.Is(backupErr, os.ErrNotExist) {
				if backupErr := writeExclusiveFile(backup, current, 0o600); backupErr != nil {
					return fmt.Errorf("back up persistent Addressables %s: %w", file.name, backupErr)
				}
			} else if backupErr != nil {
				return fmt.Errorf("inspect persistent Addressables %s backup: %w", file.name, backupErr)
			}
		}
		if err := replaceCatalog(target, file.data); err != nil {
			return fmt.Errorf("install persistent Addressables %s: %w", file.name, err)
		}
	}
	return nil
}

func readCatalog(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open local ServerData catalog: %w", err)
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maximumCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read local ServerData catalog: %w", err)
	}
	if len(raw) == 0 || len(raw) > maximumCatalogBytes {
		return nil, errors.New("local ServerData catalog has an invalid size")
	}
	return raw, nil
}

type managedBundle struct {
	Path           string `json:"path"`
	SourceSize     int64  `json:"source_size"`
	SourceModified int64  `json:"source_modified"`
	TargetSize     int64  `json:"target_size"`
	TargetModified int64  `json:"target_modified"`
}

func managedCatalogBundles(aa string) (map[string]managedBundle, error) {
	managed := map[string]managedBundle{}
	raw, err := os.ReadFile(filepath.Join(aa, localBundleManifest))
	if errors.Is(err, os.ErrNotExist) {
		return managed, nil
	}
	if err != nil {
		return nil, err
	}
	var records []managedBundle
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("read managed Addressables bundles: %w", err)
	}
	for _, record := range records {
		if _, err := resourcecatalog.BundlePath(resourcecatalog.RemotePrefix + "/platform/HD/version/" + record.Path); err != nil {
			return nil, err
		}
		managed[record.Path] = record
	}
	return managed, nil
}

func localizeCatalog(raw []byte, release, aa string, managed map[string]managedBundle) ([]byte, error) {
	document, ids, err := resourcecatalog.Decode(raw)
	if err != nil {
		return nil, err
	}
	remoteCount := 0
	type bundleLink struct{ source, destination, relative string }
	var links []bundleLink
	for index, internalID := range ids {
		if strings.HasPrefix(internalID, resourcecatalog.RemotePrefix) {
			relative, err := resourcecatalog.BundlePath(internalID)
			if err != nil {
				return nil, err
			}
			source, err := catalogFilePath(release, relative)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(source)
			if err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("local ServerData catalog references a missing bundle: %s", source)
			}
			destination, err := catalogFilePath(aa, relative)
			if err != nil {
				return nil, err
			}
			links = append(links, bundleLink{source, destination, relative})
			ids[index] = localCatalogPrefix + strings.ReplaceAll(filepath.ToSlash(relative), "/", "\\")
			remoteCount++
			continue
		}
		if relative, ok := strings.CutPrefix(internalID, localCatalogPrefix); ok {
			relative = strings.ReplaceAll(relative, "\\", "/")
			path, err := catalogFilePath(aa, relative)
			if err != nil {
				return nil, err
			}
			if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("built-in Addressables catalog references a missing local bundle: %s", relative)
			}
		}
	}
	if remoteCount == 0 {
		return nil, errors.New("local ServerData catalog has no CDN bundle entries")
	}
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	document["m_InternalIds"] = encodedIDs
	localized, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode localized ServerData catalog: %w", err)
	}
	for _, link := range links {
		if err := ensureCatalogBundleLink(link.source, link.destination, managed[link.relative]); err != nil {
			return nil, err
		}
	}
	return localized, nil
}

func ensureCatalogBundleLink(source, destination string, managed managedBundle) error {
	sourceInfo, err := os.Stat(source)
	if err != nil || !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("local ServerData catalog references a missing bundle: %s", source)
	}
	if destinationInfo, err := os.Lstat(destination); err == nil {
		if !destinationInfo.Mode().IsRegular() {
			return fmt.Errorf("game Addressables bundle conflicts with the selected local release: %s", destination)
		}
		if os.SameFile(sourceInfo, destinationInfo) {
			return nil
		}
		if managed.Path != "" && sourceInfo.Size() == managed.SourceSize && sourceInfo.ModTime().UnixNano() == managed.SourceModified && destinationInfo.Size() == managed.TargetSize && destinationInfo.ModTime().UnixNano() == managed.TargetModified {
			return nil
		}
		if sourceInfo.Size() == destinationInfo.Size() {
			sourceHash, err := util.SHA256File(source)
			if err != nil {
				return err
			}
			targetHash, err := util.SHA256File(destination)
			if err != nil {
				return err
			}
			if sourceHash == targetHash {
				return nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect game Addressables bundle: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create game Addressables bundle directory: %w", err)
	}
	staged, err := os.CreateTemp(filepath.Dir(destination), ".bundle-local-*")
	if err != nil {
		return err
	}
	path := staged.Name()
	defer func() { _ = staged.Close(); _ = os.Remove(path) }()
	if err := staged.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := os.Link(source, path); err != nil {
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(out, in)
		syncErr, closeErr := out.Sync(), out.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return err
		}
		if written != sourceInfo.Size() {
			return errors.New("incomplete local Addressables bundle copy")
		}
	}
	return util.ReplaceFile(path, destination)
}

func catalogFilePath(root, relative string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if !filepath.IsLocal(filepath.FromSlash(relative)) {
		return "", errors.New("catalog file path must be relative to its directory")
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("catalog file escapes its directory")
	}
	for path := target; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("catalog path contains a symbolic link: %s", path)
			}
			if path == target && !info.Mode().IsRegular() || path != target && !info.IsDir() {
				return "", fmt.Errorf("catalog path conflicts with a non-regular file or directory: %s", path)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if path == root {
			break
		}
	}
	return target, nil
}

func resolvedCatalogPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for _, part := range slices.Backward(suffix) {
				resolved = filepath.Join(resolved, part)
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}

func separateCatalogDirectories(release string, outputs ...string) error {
	source, err := resolvedCatalogPath(release)
	if err != nil {
		return err
	}
	for _, output := range outputs {
		target, err := resolvedCatalogPath(output)
		if err != nil {
			return err
		}
		for _, pair := range [][2]string{{source, target}, {target, source}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("local resource source and game output directories must not overlap")
			}
		}
	}
	return nil
}

func writeExclusiveFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func replaceCatalog(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".catalog-local-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
