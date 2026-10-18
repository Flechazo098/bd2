package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/resourcecatalog"
)

const localCatalogReceipt = ".bd2-local-catalog.json"

type catalogFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type catalogReceipt struct {
	Schema  int           `json:"schema"`
	Release string        `json:"release"`
	Cache   string        `json:"cache"`
	Files   []catalogFile `json:"files"`
}

func catalogStamp(path string) (catalogFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return catalogFile{}, err
	}
	if !info.Mode().IsRegular() {
		return catalogFile{}, fmt.Errorf("local catalog requires a regular file: %s", path)
	}
	return catalogFile{Path: path, Size: info.Size(), Modified: info.ModTime().UnixNano()}, nil
}

func catalogStateFiles(release, aa, cache string) []string {
	return []string{
		filepath.Join(release, "catalog_alpha.json"), filepath.Join(release, "catalog_alpha.hash"),
		filepath.Join(aa, "catalog.json"), filepath.Join(aa, localBundleManifest),
		filepath.Join(cache, "catalog_alpha.json"), filepath.Join(cache, "catalog_alpha.hash"),
	}
}

func localCatalogCurrent(release, aa, cache string) bool {
	receipt, err := commonconfig.Load(filepath.Join(aa, localCatalogReceipt), catalogReceipt{}, commonconfig.DecodeOptions{RejectTrailing: true})
	paths := catalogStateFiles(release, aa, cache)
	if err != nil || receipt.Schema != 1 || receipt.Release != release || receipt.Cache != cache || len(receipt.Files) < len(paths) {
		return false
	}
	for i, file := range receipt.Files {
		if i < len(paths) && file.Path != paths[i] {
			return false
		}
		current, err := catalogStamp(file.Path)
		if err != nil || current != file {
			return false
		}
	}
	managed, err := managedCatalogBundles(aa)
	if err != nil || len(managed) == 0 {
		return false
	}
	for relative, record := range managed {
		for _, file := range []catalogFile{
			{Path: filepath.Join(release, filepath.FromSlash(relative)), Size: record.SourceSize, Modified: record.SourceModified},
			{Path: filepath.Join(aa, filepath.FromSlash(relative)), Size: record.TargetSize, Modified: record.TargetModified},
		} {
			current, err := catalogStamp(file.Path)
			if err != nil || current != file {
				return false
			}
		}
	}
	return true
}

func recordLocalCatalog(release, aa, cache string, raw []byte) error {
	managed, err := managedCatalogBundles(aa)
	if err != nil {
		return err
	}
	_, ids, err := resourcecatalog.Decode(raw)
	if err != nil {
		return err
	}
	paths := catalogStateFiles(release, aa, cache)
	for _, id := range ids {
		if relative, ok := strings.CutPrefix(id, localCatalogPrefix); ok {
			relative = strings.ReplaceAll(relative, "\\", "/")
			if _, exists := managed[relative]; !exists {
				paths = append(paths, filepath.Join(aa, filepath.FromSlash(relative)))
			}
		}
	}
	receipt := catalogReceipt{Schema: 1, Release: release, Cache: cache}
	for _, path := range paths {
		file, err := catalogStamp(path)
		if err != nil {
			return err
		}
		receipt.Files = append(receipt.Files, file)
	}
	return commonconfig.Save(filepath.Join(aa, localCatalogReceipt), receipt, commonconfig.WriteOptions{})
}
