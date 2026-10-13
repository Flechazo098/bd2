//go:build !release

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevelopmentPlanDoesNotCreateDataOrReadConfiguration(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "server-data")
	args := []string{"--dev", "run", "--plan", "--data-dir", data, "--version-config", filepath.Join(root, "missing-versions.json")}
	if handled, err := runDevelopmentCommand(args); !handled || err != nil {
		t.Fatalf("inspection attempted to start the server with nonexistent inputs: %v, %v", handled, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("server inspection created data or configuration: %v, %v", entries, err)
	}
}
