package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	clientconfig "bd2server/internal/client/config"
	"bd2server/internal/client/plugin"
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/release"
	"bd2server/internal/common/versionconfig"
)

func TestGameReceiptRechecksContentWhenInstalledMetadataChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "resource.bundle")
	if err := os.WriteFile(path, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt := gameReceipt{Fingerprint: "target", Files: []verifiedGameFile{{Path: path, Size: 7, Modified: info.ModTime().UnixNano(), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("trusted")))}}}
	if err := commonconfig.Save(filepath.Join(root, gameReceiptFile), receipt, commonconfig.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !gameReceiptMatches(root, "target") {
		t.Fatal("verified installation was not recognized")
	}
	if gameReceiptMatches(root, "different-target") {
		t.Fatal("receipt accepted a different required fingerprint")
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	if gameReceiptMatches(root, "target") {
		t.Fatal("same-length content corruption was trusted")
	}
	if err := os.WriteFile(path, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	if !gameReceiptMatches(root, "target") {
		t.Fatal("unchanged verified bytes were rejected after a metadata-only change")
	}
}

func TestServerOnlyFingerprintChangeReusesVerifiedGameResources(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "resource.bundle")
	if err := os.WriteFile(path, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	resourceID := strings.Repeat("a", 64)
	previous := gameReceipt{Fingerprint: strings.Repeat("1", 64), ResourceFingerprint: resourceID, GameVersion: "2.36.7", BundleVersion: "20261007175947", GameDataVersion: "20261008155037", Files: []verifiedGameFile{{Path: path, Size: 7, Modified: info.ModTime().UnixNano(), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("trusted")))}}}
	if err := commonconfig.Save(filepath.Join(root, gameReceiptFile), previous, commonconfig.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Directory: t.TempDir()})
	m.fingerprint = &release.Fingerprint{ID: strings.Repeat("2", 64), GameFingerprint: resourceID, RequireGameUpdate: true, Game: &release.GameResources{GameVersion: "2.36.7"}}
	if err := m.ensureGame(context.Background(), root); err != nil {
		t.Fatalf("unchanged resource identity required a game download or process inspection: %v", err)
	}
	actual, err := readGameReceipt(root)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Fingerprint != m.fingerprint.ID || actual.ResourceFingerprint != resourceID || actual.GameVersion != previous.GameVersion || len(actual.Files) != 1 {
		t.Fatalf("server-only update lost verified game state: %+v", actual)
	}
}

func TestDevelopmentGameUsesProjectFingerprintAndNeverInstallsServerRequirement(t *testing.T) {
	game := t.TempDir()
	versions := versionconfig.Config{ServerVersion: "2.37.13+server.0.2.0", ClientVersion: "2.37.13+client.0.2.7", GameVersion: "2.37.13", BundleVersion: "20261022174708", GameDataVersion: "20261021182514"}
	m := New(Options{Directory: t.TempDir(), Versions: versions, Development: true, DevelopmentPlugins: plugin.Set{{ID: "future_plugin", Name: "UnrelatedFileName.dll"}}})
	m.project = &release.Fingerprint{ID: "project", ServerVersion: versions.ServerVersion, ClientVersion: versions.ClientVersion, GameFingerprint: "project-resources"}
	m.fingerprint = &release.Fingerprint{ID: "server", RequireGameUpdate: true}
	err := m.ensureGame(context.Background(), game)
	if err == nil || !strings.Contains(err.Error(), "update the project's") {
		t.Fatalf("development launch accepted a server requirement the project does not describe: %v", err)
	}
	if entries, err := os.ReadDir(game); err != nil || len(entries) != 0 {
		t.Fatalf("development preparation wrote into the game directory: %v %v", entries, err)
	}
	got, err := m.Runtime(game)
	if err != nil {
		t.Fatal(err)
	}
	want := clientconfig.Runtime{ClientVersion: versions.ClientVersion, GameVersion: versions.GameVersion, BundleVersion: versions.BundleVersion, GameDataVersion: versions.GameDataVersion, UpdateFingerprint: "project", ResourceFingerprint: "project-resources", PluginFiles: map[string]string{"future_plugin": "UnrelatedFileName.dll"}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("development runtime = %+v, want project configuration %+v", *got, want)
	}
}
