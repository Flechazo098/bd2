package versionconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndResolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	data := []byte(`{
  "game_version":"2.35.10",
  "client_version":"2.35.10+client.0.1.0",
  "server_version":"2.35.10+server.0.1.0",
  "game_data_version":"20260923193640",
  "bundle_version":"20260921135230",
  "seed_directory":"go/seed/v2_35_10",
  "plugins":{"local_identity":"0.6.0","capture_environment":"0.2.0","login_ui":"0.1.0"}
}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GameVersion != "2.35.10" || cfg.ClientVersion != "2.35.10+client.0.1.0" || cfg.ServerVersion != "2.35.10+server.0.1.0" || cfg.Plugins.LoginUI != "0.1.0" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	want := filepath.Join(dir, "go", "seed", "v2_35_10")
	if got := cfg.Resolve(cfg.SeedDirectory); got != want {
		t.Fatalf("Resolve()=%q, want %q", got, want)
	}
}

func TestLoadRejectsUnknownAndEscapingFields(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":       `{"game_version":"2.35.10","unknown":true}`,
		"escape":        `{"game_version":"2.35.10","client_version":"2.35.10+client.0.1.0","server_version":"2.35.10+server.0.1.0","game_data_version":"20260923193640","bundle_version":"20260921135230","seed_directory":"../seed","plugins":{"local_identity":"0.6.0","capture_environment":"0.2.0","login_ui":"0.1.0"}}`,
		"client-prefix": `{"game_version":"2.35.10","client_version":"2.36.0+client.0.1.0","server_version":"2.35.10+server.0.1.0","game_data_version":"20260923193640","bundle_version":"20260921135230","seed_directory":"seed","plugins":{"local_identity":"0.6.0","capture_environment":"0.2.0","login_ui":"0.1.0"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("accepted invalid version config")
			}
		})
	}
}

func TestFindUsesEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	data := []byte(`{
  "game_version":"9.8.7",
  "client_version":"9.8.7+client.1.2.3",
  "server_version":"9.8.7+server.4.5.6",
  "game_data_version":"20260102030405",
  "bundle_version":"20260504030201",
  "seed_directory":"seed/current",
  "plugins":{"local_identity":"1.2.3","capture_environment":"4.5.6","login_ui":"7.8.9"}
}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BD2_VERSION_CONFIG", path)
	cfg, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourcePath != path || cfg.GameVersion != "9.8.7" || cfg.ClientVersion != "9.8.7+client.1.2.3" || cfg.ServerVersion != "9.8.7+server.4.5.6" || cfg.GameDataVersion != "20260102030405" {
		t.Fatalf("Find() ignored environment override: %+v", cfg)
	}
}
