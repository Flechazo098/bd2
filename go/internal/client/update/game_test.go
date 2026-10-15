package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/release"
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
