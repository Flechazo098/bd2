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

func TestExplicitProxyNormalization(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{"", ""}, {"  ", ""}, {" http://127.0.0.1:12451/ ", "http://127.0.0.1:12451"}, {"http://[::1]:8080", "http://[::1]:8080"}, {"http://proxy.example:08080", "http://proxy.example:8080"},
	} {
		got, err := NormalizeProxyURL(test.raw)
		if err != nil || got != test.want {
			t.Errorf("normalize proxy: got %q, %v", got, err)
		}
	}
	for _, bad := range []string{"http://proxy", "https://proxy:443", "socks5://proxy:1080", "http://user:secret@proxy:8080", "http://proxy:0", "http://proxy:65536", "http://proxy:http", "http://proxy:+80", "http://proxy:8080/path", "http://proxy:8080?secret", "http://proxy:8080?", "http://proxy:8080#", "http://proxy:8080#secret", "http://:8080", "http://[::1%25zone]:8080"} {
		if _, err := NormalizeProxyURL(bad); err == nil {
			t.Errorf("invalid proxy accepted: %q", bad)
		}
	}
}

func TestProxySettingsPersistence(t *testing.T) {
	dir := t.TempDir()
	saved, err := Save(dir, Settings{ServerOrigin: "http://127.0.0.1:8080", CDNMode: CDNOfficial, ProxyURL: "http://localhost:12451/"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.ProxyURL != "http://localhost:12451" || saved != loaded {
		t.Fatalf("proxy not persisted: %#v, %v", loaded, err)
	}
	if _, err := Save(dir, Settings{ServerOrigin: "http://127.0.0.1:8080", CDNMode: CDNOfficial}); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(dir)
	if err != nil || loaded.ProxyURL != "" {
		t.Fatal("omitted proxy should use direct connection")
	}
}
