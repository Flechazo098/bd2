package main

import (
	"path/filepath"
	"testing"
)

func TestDevelopmentSettingsDefaultFollowsServerDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "custom-data")
	if got := resolveDevelopmentSettingsPath(dataDir, ""); got != filepath.Join(dataDir, "dev-tools.json") {
		t.Fatalf("development settings path = %q", got)
	}
	override := filepath.Join(t.TempDir(), "operator-settings.json")
	if got := resolveDevelopmentSettingsPath(dataDir, override); got != override {
		t.Fatalf("explicit development settings path = %q, want %q", got, override)
	}
}
