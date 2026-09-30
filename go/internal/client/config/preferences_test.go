package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreferencesRoundTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	game := filepath.Join(root, "game")
	if err := SavePreferences(game); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPreferences()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(game)
	if got.SchemaVersion != preferencesSchemaVersion || got.GameDirectory != want {
		t.Fatalf("preferences=%+v", got)
	}
	path, err := PreferencesPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
