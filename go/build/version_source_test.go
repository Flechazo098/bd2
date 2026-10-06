package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func versionSourceFixture(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"game_version": "2.35.10", "client_version": "2.35.10+client.0.1.0", "server_version": "2.35.10+server.0.1.0",
		"game_data_version": "20260923193640", "bundle_version": "20260921135230", "seed_directory": "go/seed/v2_35_10",
		"plugins": map[string]any{"local_identity": "0.6.1", "capture_environment": "0.2.0", "login_ui": "0.2.0", "cash_shop": "0.1.2"},
	}
}

func writeVersionFixture(t *testing.T, path string, config map[string]any) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateVersionSourcePreservesUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "versions with spaces.json")
	output := filepath.Join(dir, "generated with spaces", "Version.cs")
	writeVersionFixture(t, config, versionSourceFixture(t))
	args := []string{"--config", config, "--output", output, "--plugin", "cash_shop"}
	if err := (task{root: dir}).generateVersionSource(args); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `Plugin = "0.1.2";`) || !strings.Contains(string(raw), `ClientRelease = "2.35.10+client.0.1.0";`) {
		t.Fatalf("incorrect generated versions: %s", raw)
	}
	stamp := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(output, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := (task{root: dir}).generateVersionSource(args); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || !info.ModTime().Equal(stamp) {
		t.Fatalf("unchanged source rewritten: info=%v err=%v", info, err)
	}
	changed := versionSourceFixture(t)
	changed["plugins"].(map[string]any)["cash_shop"] = "0.1.3"
	writeVersionFixture(t, config, changed)
	if err := (task{root: dir}).generateVersionSource(args); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(output)
	if err != nil || !strings.Contains(string(raw), `Plugin = "0.1.3";`) {
		t.Fatalf("changed source not installed: %s err=%v", raw, err)
	}
}

func TestGenerateVersionSourceRejectsInvalidConfigWithoutWriting(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
		plugin string
	}{
		{"cross-game-client", func(c map[string]any) { c["client_version"] = "2.34.13+client.0.1.0" }, "login_ui"},
		{"wrong-server-component", func(c map[string]any) { c["server_version"] = "2.35.10+client.0.1.0" }, "login_ui"},
		{"invalid-resource", func(c map[string]any) { c["bundle_version"] = "2026092113523" }, "login_ui"},
		{"injected-plugin-source", func(c map[string]any) { c["plugins"].(map[string]any)["cash_shop"] = "1.0.0\";" }, "cash_shop"},
		{"invalid-unselected-plugin", func(c map[string]any) { c["plugins"].(map[string]any)["cash_shop"] = "invalid" }, "login_ui"},
		{"unknown-field", func(c map[string]any) { c["game_versoin"] = "2.35.10" }, "login_ui"},
		{"escaping-seed", func(c map[string]any) { c["seed_directory"] = `..\outside` }, "login_ui"},
		{"unknown-plugin", func(map[string]any) {}, "game_names"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "versions.json")
			output := filepath.Join(dir, "Version.cs")
			c := versionSourceFixture(t)
			test.change(c)
			writeVersionFixture(t, config, c)
			prior := []byte("existing generated source")
			if err := os.WriteFile(output, prior, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := (task{}).generateVersionSource([]string{"--config", config, "--output", output, "--plugin", test.plugin}); err == nil {
				t.Fatal("invalid configuration accepted")
			}
			raw, err := os.ReadFile(output)
			if err != nil || string(raw) != string(prior) {
				t.Fatalf("validation failure modified output=%s err=%v", raw, err)
			}
		})
	}
}
