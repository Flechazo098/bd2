package resourcefetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/release"
	"bd2server/internal/common/resourcecatalog"
	"bd2server/internal/server/design/gamedata"
)

func fetchPinned(ctx context.Context, options Options) (Manifest, error) {
	g := options.Pinned
	if err := g.Validate(); err != nil {
		return Manifest{}, err
	}
	if options.OutputRoot == "" || len(g.ResourceFiles) == 0 || len(g.ResourceOrigins) == 0 {
		return Manifest{}, errors.New("resource fetch: signed inventory, origins and output directory are required")
	}
	root, err := filepath.Abs(options.OutputRoot)
	if err != nil {
		return Manifest{}, err
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 30 * time.Minute}
	}
	if cached, ok := Prepared(root, *g); ok {
		return cached, nil
	}
	pins := make(map[string]release.ResourceFile, len(g.ResourceFiles))
	for index, pin := range g.ResourceFiles {
		pins[pin.Path] = pin
		target := filepath.Join(root, filepath.FromSlash(pin.Path))
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return Manifest{}, errors.New("resource fetch: unsafe signed resource path")
		}
		if release.Matches(target, release.File{Size: pin.Size, SHA256: pin.SHA256}) {
			continue
		}
		var last error
		for _, origin := range g.ResourceOrigins {
			_, last = downloadFile(ctx, options.Client, strings.TrimRight(origin, "/")+"/"+escapePath(pin.Path), target, pin)
			if last == nil {
				break
			}
		}
		if last != nil {
			return Manifest{}, fmt.Errorf("resource fetch: %s: %w", pin.Path, last)
		}
		if (index+1)%25 == 0 || index+1 == len(g.ResourceFiles) {
			options.log(fmt.Sprintf("Resources verified %d/%d", index+1, len(g.ResourceFiles)))
		}
	}
	var design struct {
		Version json.RawMessage `json:"version"`
	}
	designRaw, err := os.ReadFile(filepath.Join(root, "GameData", g.GameDataVersion, "design.version"))
	if err != nil {
		return Manifest{}, err
	}
	if err := json.Unmarshal(bytes.TrimPrefix(designRaw, []byte{0xef, 0xbb, 0xbf}), &design); err != nil {
		return Manifest{}, err
	}
	if strings.Trim(string(design.Version), "\"") != g.GameDataVersion {
		return Manifest{}, errors.New("resource fetch: GameData design.version mismatch")
	}
	data, err := gamedata.Validate(filepath.Join(root, "GameData"), g.GameDataVersion)
	if err != nil {
		return Manifest{}, err
	}
	base := "ServerData/" + g.Platform + "/HD/" + g.BundleVersion + "/"
	hash, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(base+"catalog_alpha.hash")))
	if err != nil {
		return Manifest{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(string(hash)), g.CatalogHash) {
		return Manifest{}, errors.New("resource fetch: catalog version hash mismatch")
	}
	catalog, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(base+"catalog_alpha.json")))
	if err != nil {
		return Manifest{}, err
	}
	paths, err := resourcecatalog.BundlePaths(catalog)
	if err != nil {
		return Manifest{}, err
	}
	var total int64
	for _, path := range paths {
		pin, ok := pins[base+path]
		if !ok {
			return Manifest{}, fmt.Errorf("resource fetch: signed inventory is missing %s", base+path)
		}
		if _, ok := validUnityBundle(filepath.Join(root, filepath.FromSlash(base+path))); !ok {
			return Manifest{}, fmt.Errorf("resource fetch: invalid bundle %s", path)
		}
		total += pin.Size
	}
	result := Manifest{BundleVersion: g.BundleVersion, GameDataVersion: g.GameDataVersion, Platform: g.Platform, GameData: data}
	result.InventoryID = g.ResourceID()
	for _, pin := range g.ResourceFiles {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(pin.Path)))
		if err != nil {
			return Manifest{}, err
		}
		result.VerifiedFiles = append(result.VerifiedFiles, VerifiedFile{Path: pin.Path, Size: pin.Size, Modified: info.ModTime().UnixNano(), SHA256: pin.SHA256})
	}
	result.ServerData.CatalogHash, result.ServerData.Bundles, result.ServerData.Bytes = g.CatalogHash, len(paths), total
	if err := commonconfig.Save(filepath.Join(root, "resource-fetch-manifest.json"), result, commonconfig.WriteOptions{FileMode: 0644}); err != nil {
		return Manifest{}, err
	}
	return result, nil
}

func Prepared(root string, g release.GameResources) (Manifest, bool) {
	value, err := commonconfig.Load(filepath.Join(root, "resource-fetch-manifest.json"), Manifest{}, commonconfig.DecodeOptions{RejectTrailing: true})
	if err != nil || value.InventoryID != g.ResourceID() || len(value.VerifiedFiles) != len(g.ResourceFiles) {
		return Manifest{}, false
	}
	expected := make(map[string]release.ResourceFile, len(g.ResourceFiles))
	for _, pin := range g.ResourceFiles {
		expected[pin.Path] = pin
	}
	seen := make(map[string]bool, len(value.VerifiedFiles))
	for _, file := range value.VerifiedFiles {
		pin, ok := expected[file.Path]
		if !ok || seen[file.Path] || pin.Size != file.Size || pin.SHA256 != file.SHA256 {
			return Manifest{}, false
		}
		seen[file.Path] = true
		target := filepath.Join(root, filepath.FromSlash(file.Path))
		info, err := os.Stat(target)
		if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			return Manifest{}, false
		}
		if info.ModTime().UnixNano() != file.Modified && !release.Matches(target, release.File{Size: file.Size, SHA256: file.SHA256}) {
			return Manifest{}, false
		}
	}
	return value, true
}
