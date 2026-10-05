//go:build !release

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindDevelopmentRootWithoutRuntimeConfigurations(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "go", "cmd")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "versions.json"), filepath.Join(root, "go", "go.mod")} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(child)
	got, err := findDevelopmentRoot()
	if err != nil || got != root {
		t.Fatalf("development root = %q, %v", got, err)
	}
}

func TestAppendDefaultFlagPreservesExplicitOverride(t *testing.T) {
	for _, args := range [][]string{{"--data-dir", "custom"}, {"--data-dir=custom"}} {
		got := appendDefaultFlag(append([]string(nil), args...), "--data-dir", "default")
		if len(got) != len(args) {
			t.Fatalf("args=%v got=%v", args, got)
		}
	}
	got := appendDefaultFlag(nil, "--data-dir", "default")
	if len(got) != 2 || got[0] != "--data-dir" || got[1] != "default" {
		t.Fatalf("default args=%v", got)
	}
}
