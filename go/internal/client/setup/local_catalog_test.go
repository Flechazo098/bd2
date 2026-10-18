package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type catalogScenario struct{ game, resources, aa, release string }

func newCatalogScenario(t *testing.T, remote ...string) catalogScenario {
	t.Helper()
	root := t.TempDir()
	s := catalogScenario{game: filepath.Join(root, "instance", "game"), resources: filepath.Join(root, "resources")}
	s.aa = filepath.Join(s.game, "BrownDust II_Data", "StreamingAssets", "aa")
	s.release = filepath.Join(s.resources, "ServerData", "StandaloneWindows64", "HD", "20261007175947")
	s.write(t, filepath.Join(s.game, "BrownDust II.exe"), "game")
	s.write(t, filepath.Join(s.game, "BrownDust II_Data", "resources.assets"), "assets")
	s.write(t, filepath.Join(root, "instance", "instance.json"), `{"schema_version":1,"id":"instance"}`)
	s.write(t, filepath.Join(s.aa, "catalog.json"), `{"m_InternalIds":["{UnityEngine.AddressableAssets.Addressables.RuntimePath}\\builtin.bundle"]}`)
	s.write(t, filepath.Join(s.aa, "builtin.bundle"), "builtin bytes")
	ids := []string{localCatalogPrefix + "builtin.bundle"}
	for _, relative := range remote {
		ids = append(ids, "{BDNetwork.CdnInfo.Info}/StandaloneWindows64/HD/20261007175947/"+relative)
	}
	raw, err := json.Marshal(map[string]any{"m_InternalIds": ids})
	if err != nil {
		t.Fatal(err)
	}
	s.write(t, filepath.Join(s.release, "catalog_alpha.json"), string(raw))
	s.write(t, filepath.Join(s.release, "catalog_alpha.hash"), "0f90a94601be05737f86a4ec7a1c889f")
	return s
}
func (s catalogScenario) write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func (s catalogScenario) install() error {
	return InstallLocalCatalog(s.game, s.resources, "20261007175947")
}
func catalogTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
func assertCatalogTree(t *testing.T, root string, before map[string][]byte) {
	t.Helper()
	after := catalogTree(t, root)
	if len(after) != len(before) {
		t.Fatalf("rejected synchronization changed file count: %d -> %d", len(before), len(after))
	}
	for path, data := range before {
		if !bytes.Equal(data, after[path]) {
			t.Fatalf("rejected synchronization changed %s", path)
		}
	}
}

func TestIncompleteCatalogDoesNotPartiallyInstallAndRetryAdoptsCopiedBundle(t *testing.T) {
	s := newCatalogScenario(t, "a-first.bundle", "z-missing.bundle")
	s.write(t, filepath.Join(s.release, "a-first.bundle"), "current first")
	s.write(t, filepath.Join(s.aa, "a-first.bundle"), "existing first")
	before := catalogTree(t, s.game)
	if err := s.install(); err == nil {
		t.Fatal("incomplete source catalog was installed")
	}
	assertCatalogTree(t, s.game, before)
	s.write(t, filepath.Join(s.release, "z-missing.bundle"), "last bundle")
	s.write(t, filepath.Join(s.aa, "a-first.bundle"), "current first")
	partial, err := os.Stat(filepath.Join(s.aa, "a-first.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Stat(filepath.Join(s.release, "a-first.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(partial, source) {
		t.Fatal("partial-install scenario must exercise an independent copy")
	}
	if err := s.install(); err != nil {
		t.Fatalf("retry rejected an unrecorded completed copy: %v", err)
	}
	after, err := os.Stat(filepath.Join(s.aa, "a-first.bundle"))
	if err != nil || !os.SameFile(partial, after) || !partial.ModTime().Equal(after.ModTime()) {
		t.Fatal("retry unnecessarily replaced the matching partial copy")
	}
	if actual, err := os.ReadFile(filepath.Join(s.aa, "z-missing.bundle")); err != nil || string(actual) != "last bundle" {
		t.Fatal("retry did not finish the remaining bundle")
	}
	if actual, err := os.ReadFile(filepath.Join(s.aa, "builtin.bundle")); err != nil || string(actual) != "builtin bytes" {
		t.Fatal("retry changed an unrelated built-in bundle")
	}
	if _, err := os.Stat(filepath.Join(s.aa, localBundleManifest)); err != nil {
		t.Fatal("completed retry did not publish its managed bundle record")
	}
}

func TestAuthoritativeRemoteBundleReplacesUnrecordedOldContent(t *testing.T) {
	s := newCatalogScenario(t, "scene.bundle")
	s.write(t, filepath.Join(s.release, "scene.bundle"), "current release")
	s.write(t, filepath.Join(s.aa, "scene.bundle"), "previous release")
	if err := s.install(); err != nil {
		t.Fatal(err)
	}
	if actual, err := os.ReadFile(filepath.Join(s.aa, "scene.bundle")); err != nil || string(actual) != "current release" {
		t.Fatal("remote catalog entry kept unrecorded outdated content")
	}
	if actual, err := os.ReadFile(filepath.Join(s.aa, "builtin.bundle")); err != nil || string(actual) != "builtin bytes" {
		t.Fatal("remote synchronization modified unrelated builtin content")
	}
	if actual, err := os.ReadFile(filepath.Join(s.release, "scene.bundle")); err != nil || string(actual) != "current release" {
		t.Fatal("synchronization modified the source resource")
	}
}

func TestCatalogRejectsSourceOutputAliasAndDirectoryConflictsBeforeCopy(t *testing.T) {
	t.Run("game output nested inside source", func(t *testing.T) {
		s := newCatalogScenario(t, "scene.bundle")
		oldGame := s.game
		s.game = filepath.Join(s.release, "instance", "game")
		s.aa = filepath.Join(s.game, "BrownDust II_Data", "StreamingAssets", "aa")
		for relative, data := range catalogTree(t, oldGame) {
			s.write(t, filepath.Join(s.game, relative), string(data))
		}
		s.write(t, filepath.Join(s.release, "instance", "instance.json"), `{"schema_version":1,"id":"instance"}`)
		s.write(t, filepath.Join(s.release, "scene.bundle"), "source must survive")
		before := catalogTree(t, s.release)
		if err := s.install(); err == nil {
			t.Fatal("game output nested inside resource input was accepted")
		}
		assertCatalogTree(t, s.release, before)
	})
	t.Run("source aliases game output", func(t *testing.T) {
		s := newCatalogScenario(t, "scene.bundle")
		for _, name := range []string{"catalog_alpha.json", "catalog_alpha.hash"} {
			data, err := os.ReadFile(filepath.Join(s.release, name))
			if err != nil {
				t.Fatal(err)
			}
			s.write(t, filepath.Join(s.aa, name), string(data))
		}
		s.write(t, filepath.Join(s.aa, "scene.bundle"), "aliased source")
		if err := os.RemoveAll(s.release); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(s.aa, s.release); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		before := catalogTree(t, s.aa)
		if err := s.install(); err == nil {
			t.Fatal("source/output directory alias was accepted")
		}
		assertCatalogTree(t, s.aa, before)
	})
	t.Run("target directory", func(t *testing.T) {
		s := newCatalogScenario(t, "a-first.bundle", "z-conflict.bundle")
		s.write(t, filepath.Join(s.release, "a-first.bundle"), "first")
		s.write(t, filepath.Join(s.release, "z-conflict.bundle"), "last")
		if err := os.Mkdir(filepath.Join(s.aa, "z-conflict.bundle"), 0700); err != nil {
			t.Fatal(err)
		}
		before := catalogTree(t, s.game)
		if err := s.install(); err == nil {
			t.Fatal("bundle target directory was overwritten")
		}
		assertCatalogTree(t, s.game, before)
	})
	t.Run("target symbolic link", func(t *testing.T) {
		s := newCatalogScenario(t, "scene.bundle")
		s.write(t, filepath.Join(s.release, "scene.bundle"), "release bytes")
		other := filepath.Join(t.TempDir(), "outside.bundle")
		s.write(t, other, "keep outside")
		if err := os.Symlink(other, filepath.Join(s.aa, "scene.bundle")); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		if err := s.install(); err == nil {
			t.Fatal("bundle target symbolic link was accepted")
		}
		if actual, err := os.ReadFile(other); err != nil || string(actual) != "keep outside" {
			t.Fatal("target alias modified an unrelated file")
		}
		if _, err := os.Stat(filepath.Join(s.aa, localBundleManifest)); !os.IsNotExist(err) {
			t.Fatal("rejected alias published a managed manifest")
		}
	})
}

func TestLocalCatalogRefreshReplacesManagedBundlesAndPreservesBuiltins(t *testing.T) {
	root := t.TempDir()
	resources := t.TempDir()
	if runtime.GOOS == "windows" {
		if _, err := os.Stat("E:\\"); err == nil && !strings.EqualFold(filepath.VolumeName(root), "E:") {
			crossDrive, err := os.MkdirTemp("E:\\", "bd2-localcatalog-test-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(crossDrive); err != nil {
					t.Error(err)
				}
			})
			resources = crossDrive
			t.Logf("checking cross-volume resource copies: game=%s resources=%s", filepath.VolumeName(root), filepath.VolumeName(resources))
		}
	}
	game := filepath.Join(root, "instance", "game")
	aa := filepath.Join(game, "BrownDust II_Data", "StreamingAssets", "aa")
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(game, "BrownDust II.exe"), "game")
	write(filepath.Join(game, "BrownDust II_Data", "resources.assets"), "assets")
	write(filepath.Join(root, "instance", "instance.json"), `{"schema_version":1,"id":"instance"}`)
	write(filepath.Join(aa, "catalog.json"), `{"m_InternalIds":["{UnityEngine.AddressableAssets.Addressables.RuntimePath}\\builtin.bundle"]}`)
	write(filepath.Join(aa, "builtin.bundle"), "keep builtin")
	const version = "20261007175947"
	release := filepath.Join(resources, "ServerData", "StandaloneWindows64", "HD", version)
	write(filepath.Join(release, "catalog_alpha.json"), `{"m_InternalIds":["{BDNetwork.CdnInfo.Info}/StandaloneWindows64/HD/20261007175947/scene.bundle","{UnityEngine.AddressableAssets.Addressables.RuntimePath}\\builtin.bundle"]}`)
	write(filepath.Join(release, "catalog_alpha.hash"), "0f90a94601be05737f86a4ec7a1c889f")
	source := filepath.Join(release, "scene.bundle")
	write(source, "old bytes")
	if err := InstallLocalCatalog(game, resources, version); err != nil {
		t.Fatal(err)
	}
	streamed := filepath.Join(aa, "scene.bundle")
	if actual, err := os.ReadFile(streamed); err != nil || string(actual) != "old bytes" {
		t.Fatalf("initial resource was not installed: %q, %v", actual, err)
	}
	previous, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	write(source+".new", "new bytes")
	changed := previous.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(source+".new", changed, changed); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source+".new", source); err != nil {
		t.Fatal(err)
	}
	if err := InstallLocalCatalog(game, resources, version); err != nil {
		t.Fatal(err)
	}
	if actual, err := os.ReadFile(streamed); err != nil || string(actual) != "new bytes" {
		t.Fatalf("managed resource kept the old source inode: %q, %v", actual, err)
	}
	if actual, err := os.ReadFile(filepath.Join(aa, "builtin.bundle")); err != nil || string(actual) != "keep builtin" {
		t.Fatalf("unrelated builtin changed: %q, %v", actual, err)
	}
	installed, err := os.Stat(streamed)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallLocalCatalog(game, resources, version); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.Stat(streamed)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(installed, unchanged) {
		t.Fatal("unchanged cached resource was unnecessarily replaced")
	}
	cache := filepath.Join(root, "instance", "unity", "persistent", "com.unity.addressables")
	stable := map[string]os.FileInfo{}
	for _, path := range []string{filepath.Join(aa, "catalog.json"), filepath.Join(aa, localBundleManifest), filepath.Join(cache, "catalog_alpha.json"), filepath.Join(cache, "catalog_alpha.hash")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		stable[path] = info
	}
	if err := InstallLocalCatalog(game, resources, version); err != nil {
		t.Fatal(err)
	}
	for path, previous := range stable {
		current, err := os.Stat(path)
		if err != nil || !os.SameFile(previous, current) || !previous.ModTime().Equal(current.ModTime()) {
			t.Fatalf("unchanged preparation rewrote %s: %v", path, err)
		}
	}
	if err := os.Remove(streamed); err != nil {
		t.Fatal(err)
	}
	if err := InstallLocalCatalog(game, resources, version); err != nil {
		t.Fatal(err)
	}
	if actual, err := os.ReadFile(streamed); err != nil || string(actual) != "new bytes" {
		t.Fatalf("cached preparation did not restore the missing current bundle: %q, %v", actual, err)
	}
	persistentCatalog := filepath.Join(cache, "catalog_alpha.json")
	if err := os.Remove(persistentCatalog); err != nil {
		t.Fatal(err)
	}
	if err := InstallLocalCatalog(game, resources, version); err != nil {
		t.Fatal(err)
	}
	gameCatalog, err := os.ReadFile(filepath.Join(aa, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := os.ReadFile(persistentCatalog); err != nil || string(actual) != string(gameCatalog) {
		t.Fatalf("cached preparation did not restore the persistent catalog: %v", err)
	}
	if actual, err := os.ReadFile(filepath.Join(aa, "builtin.bundle")); err != nil || string(actual) != "keep builtin" {
		t.Fatalf("repair modified the unrelated built-in bundle: %q, %v", actual, err)
	}
}
