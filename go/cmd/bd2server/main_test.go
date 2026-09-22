package main

import (
	"path/filepath"
	"testing"
)

func TestValidateAccountStatePathsRequiresOneTransactionDirectory(t *testing.T) {
	root := t.TempDir()
	progress := filepath.Join(root, "progress.json")
	deck := filepath.Join(root, "deck.json")
	if err := validateAccountStatePaths(root, progress, deck); err != nil {
		t.Fatal(err)
	}
	if err := validateAccountStatePaths(root, filepath.Join(root, "renamed.json"), deck); err == nil {
		t.Fatal("renamed progress escaped transaction allow-list")
	}
	if err := validateAccountStatePaths(root, progress, filepath.Join(t.TempDir(), "deck.json")); err == nil {
		t.Fatal("deck outside account transaction directory was accepted")
	}
}
