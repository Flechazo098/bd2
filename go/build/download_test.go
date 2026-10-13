package main

import (
	"path/filepath"
	"testing"
)

func TestDownloadForwardingPreservesCallerPathsAndRepoDefaults(t *testing.T) {
	root := t.TempDir()
	args, err := downloadClientArguments(root, []string{"--game-dir=game-output", "--probe-only"})
	if err != nil {
		t.Fatal(err)
	}
	wantGame, err := filepath.Abs("game-output")
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "--game-dir="+wantGame {
		t.Fatalf("game path would change after child changes directory: %v", args)
	}
	_, paths, err := absoluteDownloadPaths(args, "package-dir", "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if paths["package-dir"] != root || paths["manifest"] != filepath.Join(root, "tools", "client", "client-download.json") {
		t.Fatalf("development defaults do not point to the repository: %v", paths)
	}
	packageDir := filepath.Join(root, "release")
	args, err = downloadClientArguments(root, []string{"--package-dir", packageDir})
	if err != nil {
		t.Fatal(err)
	}
	_, paths, err = absoluteDownloadPaths(args, "package-dir", "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if paths["manifest"] != filepath.Join(packageDir, "client-download.json") {
		t.Fatalf("explicit release package used repository manifest: %v", paths)
	}
}
