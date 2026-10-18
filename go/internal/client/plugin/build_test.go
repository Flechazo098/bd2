package plugin

import (
	"bd2server/internal/common/util"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedPluginCacheSkipsSDKVersionAndCompilationStages(t *testing.T) {
	root := t.TempDir()
	game := filepath.Join(root, "game")
	write := func(relative string, value string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("game/BrownDust II.exe", "fixture")
	write("game/BrownDust II_Data/resources.assets", "fixture")
	write("game/BrownDust II_Data/Managed/Assembly-CSharp.dll", "fixture")
	write("game/BepInEx/core/BepInEx.dll", "fixture")
	write("plugins/LocalIdentity/Plugin.cs", "fixture")
	write("go/internal/client/plugin/build.go", "fixture")
	write("go/internal/buildversion/version.go", "fixture")
	write("versions.json", "invalid JSON must not be read when cache is current")
	var progress bytes.Buffer
	if err := BuildDevelopment(context.Background(), root, game, &progress); err == nil {
		t.Fatal("uncached build accepted invalid versions")
	}
	workRoot := filepath.Join(root, ".build", "client-plugins")
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	var work string
	for _, entry := range entries {
		if entry.IsDir() {
			work = filepath.Join(workRoot, entry.Name())
		}
	}
	if work == "" {
		t.Fatal("build did not establish a cache workspace")
	}
	inputs, err := snapshotInputs(root, filepath.Join(game, "BrownDust II_Data", "Managed"), filepath.Join(game, "BepInEx"), make(fileDigestCache))
	if err != nil {
		t.Fatal(err)
	}
	write("plugins/LocalIdentity/bin/Release/netstandard2.1/BD2LocalIdentity.dll", "verified-output")
	output := filepath.Join(root, "plugins", "LocalIdentity", "bin", "Release", "netstandard2.1", "BD2LocalIdentity.dll")
	digest, err := util.SHA256File(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveBuildManifest(filepath.Join(work, "build.json"), buildManifest{Input: digestInputs(inputs), Outputs: map[string]string{output: digest}}); err != nil {
		t.Fatal(err)
	}
	progress.Reset()
	if err := BuildDevelopment(context.Background(), root, game, &progress); err != nil {
		t.Fatal("cache hit ran SDK/version/compile actions", err)
	}
	if !strings.Contains(progress.String(), "inputs and outputs unchanged") {
		t.Fatal("cache was not reused", progress.String())
	}
	if _, err := os.Stat(filepath.Join(work, "compile-inputs")); !os.IsNotExist(err) {
		t.Fatal("cache hit staged compilation inputs", err)
	}
	write("plugins/LocalIdentity/bin/Release/netstandard2.1/BD2LocalIdentity.dll", "damaged-output")
	if err := BuildDevelopment(context.Background(), root, game, &progress); err == nil {
		t.Fatal("damaged plugin output bypassed version validation as a cache hit")
	}
}
