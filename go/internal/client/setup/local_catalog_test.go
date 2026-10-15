package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
}
