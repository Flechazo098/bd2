package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateLocalCatalogRequiresEveryRemoteBundle(t *testing.T) {
	release := t.TempDir()
	path := filepath.Join("nested", "current.bundle")
	if err := os.MkdirAll(filepath.Join(release, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, path), []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"m_InternalIds": []string{
		remoteCatalogPrefix + `StandaloneWindows64\HD\version\nested/current.bundle`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLocalCatalog(raw, release); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(release, path)); err != nil {
		t.Fatal(err)
	}
	if err := validateLocalCatalog(raw, release); err == nil {
		t.Fatal("catalog with missing bundle unexpectedly passed")
	}
}

func TestCatalogBundlePathRejectsTraversal(t *testing.T) {
	if _, err := catalogBundlePath(remoteCatalogPrefix + `StandaloneWindows64\HD\version\..\escape.bundle`); err == nil {
		t.Fatal("catalog traversal path unexpectedly passed")
	}
}

func TestReplaceCatalogReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceCatalog(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("catalog=%q", got)
	}
}

func TestLocalizeCatalogPreservesMetadataAndCreatesHardLink(t *testing.T) {
	release := t.TempDir()
	aa := t.TempDir()
	relative := filepath.Join("nested", "current.bundle")
	if err := os.MkdirAll(filepath.Join(release, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(release, relative)
	if err := os.WriteFile(source, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"m_BuildResultHash": "current-metadata",
		"m_InternalIds": []string{
			remoteCatalogPrefix + `StandaloneWindows64\HD\version\nested/current.bundle`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	localized, err := localizeCatalog(raw, release, aa)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(localized, []byte(`"m_BuildResultHash":"current-metadata"`)) ||
		!bytes.Contains(localized, []byte(`Addressables.RuntimePath`)) {
		t.Fatalf("localized catalog=%s", localized)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	destinationInfo, err := os.Stat(filepath.Join(aa, relative))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sourceInfo, destinationInfo) {
		t.Fatal("localized bundle is not a hard link to the selected release")
	}
}

func TestSynchronizePersistentCatalogBacksUpAndIsIdempotent(t *testing.T) {
	cache := t.TempDir()
	oldCatalog := []byte("old catalog")
	oldHash := []byte("old hash")
	if err := os.WriteFile(filepath.Join(cache, "catalog_alpha.json"), oldCatalog, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "catalog_alpha.hash"), oldHash, 0o600); err != nil {
		t.Fatal(err)
	}
	newCatalog := []byte("new catalog")
	newHash := []byte("new hash")
	if err := synchronizePersistentCatalog(cache, newCatalog, newHash); err != nil {
		t.Fatal(err)
	}
	if err := synchronizePersistentCatalog(cache, newCatalog, newHash); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{
		"catalog_alpha.json":                       newCatalog,
		"catalog_alpha.hash":                       newHash,
		"catalog_alpha.json.bd2-before-local-sync": oldCatalog,
		"catalog_alpha.hash.bd2-before-local-sync": oldHash,
	} {
		got, err := os.ReadFile(filepath.Join(cache, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s=%q, want %q", name, got, want)
		}
	}
}
