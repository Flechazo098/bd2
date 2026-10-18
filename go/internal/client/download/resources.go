package download

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"bd2server/internal/common/release"
	"bd2server/internal/common/resourcecatalog"
)

func (d *downloader) resources(ctx context.Context, root string, m manifest, serverPath, dataPath string, serverOrigins, dataOrigins []string, workers int) error {
	pins := make(map[string]release.ResourceFile, len(m.ResourceFiles))
	usedPins := make(map[string]bool, len(m.ResourceFiles))
	for _, pin := range m.ResourceFiles {
		pins[pin.Path] = pin
	}
	serverRanked, dataRanked := false, false
	receive := func(files []file, origins []string, workers int) error {
		if len(pins) != 0 {
			for i := range files {
				relative, err := filepath.Rel(root, files[i].target)
				if err != nil {
					return err
				}
				pin, ok := pins[filepath.ToSlash(relative)]
				if !ok {
					return fmt.Errorf("signed resource inventory is missing %s", relative)
				}
				files[i].digest, files[i].size = pin.SHA256, pin.Size
				usedPins[pin.Path] = true
			}
		}
		var missing []file
		for _, item := range files {
			if !d.verified(item) {
				missing = append(missing, item)
			}
		}
		if len(missing) == 0 {
			return d.saveRecords()
		}
		if d.measureOrigins {
			if strings.HasPrefix(missing[0].remote, dataPath+"/") {
				if !dataRanked {
					var err error
					dataOrigins, err = d.fastest(ctx, origins, missing[0].remote, "GameData")
					if err != nil {
						return err
					}
					dataRanked = true
				}
				origins = dataOrigins
			} else {
				if !serverRanked {
					var err error
					serverOrigins, err = d.fastest(ctx, origins, missing[0].remote, "ServerData")
					if err != nil {
						return err
					}
					serverRanked = true
				}
				origins = serverOrigins
			}
		}
		return d.receiveAll(ctx, missing, origins, workers)
	}
	serverDir := filepath.Join(root, "ServerData", m.Platform, "HD", m.BundleVersion)
	dataDir := filepath.Join(root, "GameData", m.GameDataVersion)
	catalog := filepath.Join(serverDir, "catalog_alpha.json")
	hashPath := filepath.Join(serverDir, "catalog_alpha.hash")
	if err := receive([]file{{remote: serverPath + "/catalog_alpha.json", target: catalog, digest: m.CatalogSHA256, size: m.CatalogBytes}, {remote: serverPath + "/catalog_alpha.hash", target: hashPath}}, serverOrigins, 2); err != nil {
		return err
	}
	hash, err := os.ReadFile(hashPath)
	if err != nil {
		return err
	}
	actualHash := strings.TrimSpace(string(hash))
	if !regexp.MustCompile(`^[a-fA-F0-9]{32}$`).MatchString(actualHash) || (m.CatalogHash != "" && !strings.EqualFold(actualHash, m.CatalogHash)) {
		return errors.New("ServerData catalog hash does not match the selected version")
	}
	raw, err := os.ReadFile(catalog)
	if err != nil {
		return err
	}
	_, ids, err := resourcecatalog.Decode(raw)
	if err != nil {
		return err
	}
	for _, id := range ids {
		parts := strings.Split(strings.ReplaceAll(id, "\\", "/"), "/")
		if parts[0] != "{BDNetwork.CdnInfo.Info}" {
			continue
		}
		if len(parts) < 5 || parts[1] != m.Platform || (parts[2] != "HD" && parts[2] != "{BDNetwork.CdnInfo.Resolution}") || (parts[3] != m.BundleVersion && parts[3] != "{BDNetwork.CdnInfo.Version}") {
			return fmt.Errorf("catalog version/platform mismatch: %s", id)
		}
	}
	paths, err := resourcecatalog.BundlePaths(raw)
	if err != nil {
		return err
	}
	if err := receive([]file{{remote: dataPath + "/design.version", target: filepath.Join(dataDir, "design.version")}, {remote: dataPath + "/release/common-dbdata.info", target: filepath.Join(dataDir, "release", "common-dbdata.info")}, {remote: dataPath + "/release/common-dbdata.bin", target: filepath.Join(dataDir, "release", "common-dbdata.bin"), digest: m.DataSHA256, size: m.DataBytes}}, dataOrigins, 3); err != nil {
		return err
	}
	var design struct {
		Version json.RawMessage `json:"version"`
	}
	designRaw, err := os.ReadFile(filepath.Join(dataDir, "design.version"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(bytes.TrimPrefix(designRaw, []byte{0xef, 0xbb, 0xbf}), &design); err != nil {
		return err
	}
	if strings.Trim(string(design.Version), "\"") != m.GameDataVersion {
		return errors.New("GameData design.version mismatch")
	}
	info, err := os.ReadFile(filepath.Join(dataDir, "release", "common-dbdata.info"))
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(string(info), "\ufeff")), 10, 64)
	if err != nil {
		return fmt.Errorf("GameData size metadata: %w", err)
	}
	stat, err := os.Stat(filepath.Join(dataDir, "release", "common-dbdata.bin"))
	if err != nil {
		return err
	}
	if size != stat.Size() {
		return errors.New("GameData size metadata mismatch")
	}
	entries, err := d.dataArchiveEntries(filepath.Join(dataDir, "release", "common-dbdata.bin"))
	if err != nil {
		return err
	}
	files := make([]file, 0, len(paths))
	for _, path := range paths {
		target, err := safeTarget(serverDir, path)
		if err != nil {
			return err
		}
		files = append(files, file{remote: serverPath + "/" + escapedPath(path), target: target, bundle: true})
	}
	if err := receive(files, serverOrigins, workers); err != nil {
		return err
	}
	var additional []file
	for _, pin := range m.ResourceFiles {
		if usedPins[pin.Path] {
			continue
		}
		target, err := safeTarget(root, pin.Path)
		if err != nil {
			return err
		}
		additional = append(additional, file{remote: "/" + escapedPath(pin.Path), target: target, digest: pin.SHA256, size: pin.Size})
	}
	if err := receive(additional, serverOrigins, workers); err != nil {
		return err
	}
	var total int64
	for _, file := range files {
		info, err := os.Stat(file.target)
		if err != nil {
			return err
		}
		total += info.Size()
	}
	metadata := map[string]any{
		"bundle_version": m.BundleVersion, "game_data_version": m.GameDataVersion, "platform": m.Platform,
		"server_data": map[string]any{"catalog_hash": strings.ToLower(actualHash), "bundles": len(files), "bytes": total},
		"game_data":   map[string]any{"version": m.GameDataVersion, "entry_count": entries, "bytes": size},
	}
	raw, err = json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := filepath.Join(root, "resource-fetch-manifest.json")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, raw) {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(path, raw)
}

func (d *downloader) dataArchiveEntries(path string) (int, error) {
	d.mu.Lock()
	r := d.records[d.receiptKey(path)]
	d.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if r.Bytes == info.Size() && r.Modified == info.ModTime().UnixNano() && r.ArchiveEntries > 0 {
		return r.ArchiveEntries, nil
	}
	entries, err := validateDataArchive(path)
	if err != nil {
		return 0, err
	}
	after, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		return 0, errors.New("GameData changed during archive validation")
	}
	r.ArchiveEntries = entries
	d.remember(path, r)
	return entries, d.saveRecords()
}
