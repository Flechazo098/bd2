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
	"strings"

	clientlayout "bd2server/internal/client/layout"
)

const (
	remoteCatalogPrefix = "{BDNetwork.CdnInfo.Info}\\"
	localCatalogPrefix  = "{UnityEngine.AddressableAssets.Addressables.RuntimePath}\\"
	localCatalogBackup  = "catalog.json.bd2-before-local-sync"
	maximumCatalogBytes = 128 << 20
)

func installCurrentLocalCatalog(gameDir, localRoot, bundleVersion string) error {
	installation, err := clientlayout.Resolve(gameDir)
	if err != nil {
		return err
	}
	release := filepath.Join(filepath.Clean(localRoot), "ServerData", "StandaloneWindows64", "HD", bundleVersion)
	source := filepath.Join(release, "catalog_alpha.json")
	sourceHash := filepath.Join(release, "catalog_alpha.hash")
	raw, err := readCatalog(source)
	if err != nil {
		return err
	}
	aa := filepath.Join(installation.Data, "StreamingAssets", "aa")
	raw, err = localizeCatalog(raw, release, aa)
	if err != nil {
		return err
	}
	target := filepath.Join(aa, "catalog.json")
	current, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read built-in Addressables catalog: %w", err)
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
	hash, err := os.ReadFile(sourceHash)
	if err != nil {
		return fmt.Errorf("read local ServerData catalog hash: %w", err)
	}
	if len(hash) == 0 || len(hash) > 1024 {
		return errors.New("local ServerData catalog hash has an invalid size")
	}
	cache, err := addressablesCacheDirectory()
	if err != nil {
		return err
	}
	return synchronizePersistentCatalog(cache, raw, hash)
}

func addressablesCacheDirectory() (string, error) {
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

func validateLocalCatalog(raw []byte, release string) error {
	_, err := localizeCatalogIDs(raw, release)
	return err
}

func localizeCatalog(raw []byte, release, aa string) ([]byte, error) {
	document, ids, err := decodeCatalog(raw)
	if err != nil {
		return nil, err
	}
	remoteCount := 0
	for index, internalID := range ids {
		if strings.HasPrefix(internalID, remoteCatalogPrefix) {
			relative, err := catalogBundlePath(internalID)
			if err != nil {
				return nil, err
			}
			source := filepath.Join(release, relative)
			destination := filepath.Join(aa, relative)
			if err := ensureCatalogBundleLink(source, destination); err != nil {
				return nil, err
			}
			ids[index] = localCatalogPrefix + strings.ReplaceAll(filepath.ToSlash(relative), "/", "\\")
			remoteCount++
			continue
		}
		if relative, ok := strings.CutPrefix(internalID, localCatalogPrefix); ok {
			relative = filepath.FromSlash(strings.ReplaceAll(relative, "\\", "/"))
			if info, err := os.Stat(filepath.Join(aa, relative)); err != nil || !info.Mode().IsRegular() {
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
	return localized, nil
}

func localizeCatalogIDs(raw []byte, release string) ([]string, error) {
	_, ids, err := decodeCatalog(raw)
	if err != nil {
		return nil, err
	}
	remoteCount := 0
	for _, internalID := range ids {
		if !strings.HasPrefix(internalID, remoteCatalogPrefix) {
			continue
		}
		relative, err := catalogBundlePath(internalID)
		if err != nil {
			return nil, err
		}
		if info, err := os.Stat(filepath.Join(release, relative)); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("local ServerData catalog references a missing bundle: %s", relative)
		}
		remoteCount++
	}
	if remoteCount == 0 {
		return nil, errors.New("local ServerData catalog has no CDN bundle entries")
	}
	return ids, nil
}

func decodeCatalog(raw []byte) (map[string]json.RawMessage, []string, error) {
	var document map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("decode local ServerData catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("local ServerData catalog must contain exactly one JSON object")
	}
	var ids []string
	if err := json.Unmarshal(document["m_InternalIds"], &ids); err != nil || len(ids) == 0 {
		return nil, nil, errors.New("local ServerData catalog has no valid internal IDs")
	}
	return document, ids, nil
}

func ensureCatalogBundleLink(source, destination string) error {
	sourceInfo, err := os.Stat(source)
	if err != nil || !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("local ServerData catalog references a missing bundle: %s", source)
	}
	if destinationInfo, err := os.Stat(destination); err == nil {
		if !destinationInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, destinationInfo) {
			return fmt.Errorf("game Addressables bundle conflicts with the selected local release: %s", destination)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect game Addressables bundle: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create game Addressables bundle directory: %w", err)
	}
	if err := os.Link(source, destination); err != nil {
		return fmt.Errorf("hard-link local bundle without duplicating it (keep the game and local resources on the same filesystem): %w", err)
	}
	return nil
}

func catalogBundlePath(internalID string) (string, error) {
	parts := strings.Split(strings.ReplaceAll(internalID, "\\", "/"), "/")
	if len(parts) < 5 {
		return "", fmt.Errorf("local ServerData catalog has an invalid CDN path: %q", internalID)
	}
	parts = parts[4:]
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `:{}`) {
			return "", fmt.Errorf("local ServerData catalog has an unsafe bundle path: %q", internalID)
		}
	}
	relative := filepath.Join(parts...)
	if !strings.EqualFold(filepath.Ext(relative), ".bundle") {
		return "", fmt.Errorf("local ServerData catalog CDN path is not a bundle: %q", internalID)
	}
	return relative, nil
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
