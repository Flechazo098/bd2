package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	clientconfig "bd2server/internal/client/config"
)

func testVersions() clientconfig.ReleaseVersions {
	return clientconfig.ReleaseVersions{
		GameVersion: "2.35.10", ClientVersion: "2.35.10+client.0.1.0", ServerVersion: "2.35.10+server.0.1.0", BundleVersion: "20260921135230", GameDataVersion: "20260923193640",
	}
}

func TestInspectRequiresGameFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Inspect(dir, testVersions()); err == nil {
		t.Fatal("accepted empty directory")
	}
	for path, data := range map[string][]byte{
		filepath.Join(dir, "BrownDust II.exe"):                        []byte("exe"),
		filepath.Join(dir, "BrownDust II_Data", "resources.assets"):   []byte("assets"),
		filepath.Join(dir, "BrownDust II_Data", "globalgamemanagers"): []byte("\x002.35.10\x00"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status, err := Inspect(dir, testVersions())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Executable || !status.Resources || status.BepInEx {
		t.Fatalf("status=%+v", status)
	}
}

func TestInspectRejectsUnsupportedClientVersion(t *testing.T) {
	dir := t.TempDir()
	for path, data := range map[string][]byte{
		filepath.Join(dir, "BrownDust II.exe"):                        []byte("exe"),
		filepath.Join(dir, "BrownDust II_Data", "resources.assets"):   []byte("assets"),
		filepath.Join(dir, "BrownDust II_Data", "globalgamemanagers"): []byte("\x002.36.0\x00"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status, err := Inspect(dir, testVersions())
	if err == nil || status.ClientVersion != "2.36.0" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestInspectMacApp(t *testing.T) {
	parent := t.TempDir()
	app := filepath.Join(parent, "BrownDust II.app")
	for path, data := range map[string][]byte{
		filepath.Join(app, "Contents", "MacOS", "BrownDust II"):                   []byte("binary"),
		filepath.Join(app, "Contents", "Resources", "Data", "resources.assets"):   []byte("assets"),
		filepath.Join(app, "Contents", "Resources", "Data", "globalgamemanagers"): []byte("\x002.35.10\x00"),
		filepath.Join(parent, "BepInEx", "core", "BepInEx.dll"):                   []byte("bepinex"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	status, err := Inspect(app, testVersions())
	if err != nil || status.GameDirectory != app || status.ClientVersion != "2.35.10" || !status.BepInEx {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestFetchResourcePolicyRejectsServerVersionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ResourcePolicy{
			Mode: clientconfig.CDNServer, ServerDataURL: "https://cdn.example/ServerData",
			GameDataURL: "https://cdn.example/GameData", BundleVersion: "wrong", GameDataVersion: "wrong",
		})
	}))
	defer server.Close()
	_, err := FetchResourcePolicy(context.Background(), server.Client(), clientconfig.Settings{ServerOrigin: server.URL, CDNMode: clientconfig.CDNServer}, testVersions())
	if err == nil {
		t.Fatal("accepted mismatched server resource versions")
	}
}

func TestFetchResourcePolicy(t *testing.T) {
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["cdn_mode"] != "server" {
			t.Errorf("request=%v", request)
		}
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(ResourcePolicy{
			Mode:            clientconfig.CDNServer,
			ServerDataURL:   "https://cdn.example/ServerData",
			GameDataURL:     "https://cdn.example/GameData",
			BundleVersion:   "20260921135230",
			GameDataVersion: "20260923193640",
		})
	}))
	defer server.Close()
	policy, err := FetchResourcePolicy(context.Background(), server.Client(), clientconfig.Settings{
		ServerOrigin: server.URL,
		CDNMode:      clientconfig.CDNServer,
	}, testVersions())
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || policy.Mode != clientconfig.CDNServer || policy.BundleVersion != "20260921135230" {
		t.Fatalf("method=%s policy=%+v", method, policy)
	}
}

func TestLocalResourcesDoNotContactServer(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		filepath.Join("ServerData", "StandaloneWindows64", "HD", "20260921135230", "catalog_alpha.json"),
		filepath.Join("ServerData", "StandaloneWindows64", "HD", "20260921135230", "catalog_alpha.hash"),
		filepath.Join("GameData", "20260923193640", "release", "common-dbdata.info"),
		filepath.Join("GameData", "20260923193640", "release", "common-dbdata.bin"),
	} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("resource"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := FetchResourcePolicy(context.Background(), nil, clientconfig.Settings{
		ServerOrigin:           "http://127.0.0.1:8080",
		CDNMode:                clientconfig.CDNLocal,
		LocalResourceDirectory: root,
	}, testVersions())
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != clientconfig.CDNLocal || policy.LocalDirectory != root || policy.ServerDataURL == "" || policy.GameDataURL == "" {
		t.Fatalf("policy=%+v", policy)
	}
}

func TestOfficialDoesNotContactServer(t *testing.T) {
	policy, err := FetchResourcePolicy(context.Background(), nil, clientconfig.Settings{
		ServerOrigin: "https://example.com",
		CDNMode:      clientconfig.CDNOfficial,
	}, testVersions())
	if err != nil || policy.Mode != clientconfig.CDNOfficial {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
}
