package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReleaseVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ReleaseFileName)
	data := `{"client_version":"2.35.10","game_data_version":"20260923193640","bundle_version":"20260921135230","seed_directory":"go/seed/v2_35_10","plugins":{"local_identity":"0.6.0","capture_environment":"0.2.0","login_ui":"0.1.0"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadReleaseVersions(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientVersion != "2.35.10" || got.BundleVersion != "20260921135230" || got.GameDataVersion != "20260923193640" {
		t.Fatalf("versions=%+v", got)
	}
}

func TestLoadReleaseVersionsRejectsUnknownAndTrailingData(t *testing.T) {
	for name, data := range map[string]string{
		"unknown":  `{"client_version":"2.35.10","unknown":true}`,
		"trailing": `{}` + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ReleaseFileName)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadReleaseVersions(path); err == nil {
				t.Fatal("accepted invalid release versions")
			}
		})
	}
}
