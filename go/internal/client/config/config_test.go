package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	got, err := Normalize(Settings{ServerOrigin: " https://example.com:8443/ ", CDNMode: CDNServer})
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || got.ServerOrigin != "https://example.com:8443" || got.CDNMode != CDNServer {
		t.Fatalf("normalized=%+v", got)
	}
	for _, bad := range []string{"example.com", "ftp://example.com", "http://192.168.1.8:8080", "https://u:p@example.com", "https://example.com/game/", "https://example.com?q=1"} {
		if _, err := Normalize(Settings{ServerOrigin: bad, CDNMode: CDNOfficial}); err == nil {
			t.Errorf("accepted origin %q", bad)
		}
	}
	localRoot := t.TempDir()
	local, err := Normalize(Settings{ServerOrigin: "http://127.0.0.1:8080", CDNMode: CDNLocal, LocalResourceDirectory: localRoot})
	if err != nil || local.LocalResourceDirectory != localRoot {
		t.Fatalf("local=%+v err=%v", local, err)
	}
	if _, err := Normalize(Settings{ServerOrigin: "http://127.0.0.1:8080", CDNMode: CDNLocal}); err == nil {
		t.Fatal("accepted local mode without a resource directory")
	}
	if _, err := Normalize(Settings{ServerOrigin: "http://127.0.0.1:8080", CDNMode: CDNOfficial, LocalResourceDirectory: localRoot}); err == nil {
		t.Fatal("accepted a local resource directory in official mode")
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	want := Settings{ServerOrigin: "http://127.0.0.1:8080", CDNMode: CDNLocal, LocalResourceDirectory: t.TempDir()}
	if _, err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerOrigin != want.ServerOrigin || got.CDNMode != want.CDNMode || got.SchemaVersion != SchemaVersion || got.LocalResourceDirectory != want.LocalResourceDirectory {
		t.Fatalf("loaded=%+v", got)
	}
	data, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "token") {
		t.Fatalf("client config unexpectedly stores a credential: %s", data)
	}
	updated := Settings{ServerOrigin: "https://friends.example:8443", CDNMode: CDNServer}
	if _, err := Save(dir, updated); err != nil {
		t.Fatalf("replace config: %v", err)
	}
	got, err = Load(dir)
	if err != nil || got.ServerOrigin != updated.ServerOrigin || got.CDNMode != updated.CDNMode {
		t.Fatalf("replaced=%+v err=%v", got, err)
	}
}

func TestSaveOmitsLocalDirectoryOutsideLocalMode(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, Settings{ServerOrigin: "https://example.com", CDNMode: CDNOfficial}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "local_resource_directory") {
		t.Fatalf("official config contains local directory field: %s", data)
	}
}

func TestPathUsesMacAppSiblingBepInEx(t *testing.T) {
	parent := t.TempDir()
	app := filepath.Join(parent, "BrownDust II.app")
	for _, path := range []string{
		filepath.Join(app, "Contents", "MacOS", "BrownDust II"),
		filepath.Join(app, "Contents", "Resources", "Data", "resources.assets"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	want := filepath.Join(parent, "BepInEx", "config", FileName)
	if got := Path(app); got != want {
		t.Fatalf("Path()=%q want=%q", got, want)
	}
}
