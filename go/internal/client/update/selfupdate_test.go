package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bd2server/internal/common/release"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func releaseFile(name, content string) release.File {
	digest := sha256.Sum256([]byte(content))
	return release.File{Name: name, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:]), URL: "/" + name}
}

func signedFixture(t *testing.T, launcher, plugins *release.Component) ([]byte, []release.TrustedKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := release.TrustedKey{ID: release.KeyID(public), PublicKey: base64.StdEncoding.EncodeToString(public)}
	manifest := release.Manifest{Schema: release.SchemaVersion, Channel: release.BuiltinChannel().Name, PublishedAt: time.Now().UTC(), Commit: strings.Repeat("a", 40), Launcher: launcher, Plugins: plugins}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(private, append([]byte("bd2-release-manifest-v1\n"), payload...))
	raw, err := json.Marshal(release.Envelope{KeyID: key.ID, Signature: base64.StdEncoding.EncodeToString(signature), Manifest: payload})
	if err != nil {
		t.Fatal(err)
	}
	return raw, []release.TrustedKey{key}
}

func assertContent(t *testing.T, path, expected string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != expected {
		t.Errorf("%s = %q, %v; want %q", path, raw, err, expected)
	}
}

func TestRunHelperWaitsForOldLauncherBeforeReplacementResultAndRestart(t *testing.T) {
	root := t.TempDir()
	target, staging := filepath.Join(root, "installed"), filepath.Join(root, "staged")
	launcherPath := filepath.Join(target, launcherExecutable())
	pid, stopLauncher := startWaitingProcess(t, launcherPath)
	oldDigest, err := release.HashFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(target, "runtime.dat"), "old runtime")
	from := "2.36.7+client.0.1.0"
	component := &release.Component{Version: "2.36.7+client.0.2.0", Files: []release.File{releaseFile(launcherExecutable(), "new executable"), releaseFile("runtime.dat", "new runtime")}}
	raw, keys := signedFixture(t, component, nil)
	writeFixture(t, filepath.Join(staging, launcherManifestFile), string(raw))
	writeFixture(t, filepath.Join(staging, launcherExecutable()), "new executable")
	writeFixture(t, filepath.Join(staging, "runtime.dat"), "new runtime")
	started := make(chan string, 1)
	start := func(path string, args ...string) error {
		if len(args) != 0 {
			return fmt.Errorf("unexpected launcher arguments: %v", args)
		}
		for _, f := range component.Files {
			if !release.Matches(filepath.Join(target, f.Name), f) {
				return fmt.Errorf("restart preceded replacement of %s", f.Name)
			}
		}
		raw, err := os.ReadFile(filepath.Join(target, ".update", resultFile))
		if err != nil {
			return fmt.Errorf("restart preceded result recording: %w", err)
		}
		var result Result
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		if !result.OK || result.From != from || result.To != component.Version || result.At.IsZero() {
			return fmt.Errorf("restart saw invalid switch result: %+v", result)
		}
		started <- path
		return nil
	}
	completed := make(chan struct{})
	var helperErr error
	go func() {
		helperErr = runHelper([]string{staging, target, strconv.Itoa(pid), from, component.Version}, keys, start)
		close(completed)
	}()
	t.Cleanup(func() {
		stopLauncher()
		select {
		case <-completed:
		case <-time.After(10 * time.Second):
			t.Error("updater did not finish after fixture launcher exit")
		}
	})
	select {
	case <-completed:
		t.Fatalf("updater finished while the old launcher was still running: %v", helperErr)
	case <-time.After(300 * time.Millisecond):
	}
	if digest, err := release.HashFile(launcherPath); err != nil || digest != oldDigest {
		t.Fatalf("running launcher was replaced: digest=%s, error=%v", digest, err)
	}
	assertContent(t, filepath.Join(target, "runtime.dat"), "old runtime")
	if _, err := os.Stat(filepath.Join(target, ".update", resultFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("switch result was recorded while the old launcher was running: %v", err)
	}
	select {
	case path := <-started:
		t.Fatalf("launcher restarted before the old process exited: %s", path)
	default:
	}
	stopLauncher()
	select {
	case <-completed:
		if helperErr != nil {
			t.Fatal(helperErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("updater did not finish after the old launcher exited and was reaped")
	}
	assertContent(t, launcherPath, "new executable")
	assertContent(t, filepath.Join(target, "runtime.dat"), "new runtime")
	select {
	case path := <-started:
		if path != launcherPath {
			t.Fatalf("restarted %q; want %q", path, launcherPath)
		}
	default:
		t.Fatal("updated launcher was not restarted")
	}
	result := TakeResult(target)
	if result == nil || !result.OK || result.From != from || result.To != component.Version {
		t.Fatalf("persisted switch result = %+v", result)
	}
}

func TestSwitchLauncherRejectsTamperedFileDespiteValidManifestSignature(t *testing.T) {
	root := t.TempDir()
	target, staging := filepath.Join(root, "installed"), filepath.Join(root, "staged")
	component := &release.Component{Version: "2.36.7+client.0.2.0", Files: []release.File{releaseFile(launcherExecutable(), "new executable"), releaseFile("runtime.dat", "new runtime")}}
	raw, keys := signedFixture(t, component, nil)
	writeFixture(t, filepath.Join(staging, launcherManifestFile), string(raw))
	writeFixture(t, filepath.Join(staging, launcherExecutable()), "new executable")
	writeFixture(t, filepath.Join(staging, "runtime.dat"), "bad runtime")
	writeFixture(t, filepath.Join(target, launcherExecutable()), "old executable")
	writeFixture(t, filepath.Join(target, "runtime.dat"), "old runtime")
	if _, err := release.Verify(raw, keys); err != nil {
		t.Fatalf("fixture signature is not valid: %v", err)
	}
	if err := switchLauncher(staging, target, "2.36.7+client.0.1.0", component.Version, keys); err == nil {
		t.Fatal("installed a corrupted staged file")
	}
	assertContent(t, filepath.Join(target, launcherExecutable()), "old executable")
	assertContent(t, filepath.Join(target, "runtime.dat"), "old runtime")
}

func TestSwitchLauncherRestoresEarlierReplacementOnRealFilesystemFailure(t *testing.T) {
	root := t.TempDir()
	target, staging := filepath.Join(root, "installed"), filepath.Join(root, "staged")
	component := &release.Component{Version: "2.36.7+client.0.2.0", Files: []release.File{releaseFile(launcherExecutable(), "new executable"), releaseFile("runtime.dat", "new runtime")}}
	raw, keys := signedFixture(t, component, nil)
	writeFixture(t, filepath.Join(staging, launcherManifestFile), string(raw))
	writeFixture(t, filepath.Join(staging, launcherExecutable()), "new executable")
	writeFixture(t, filepath.Join(staging, "runtime.dat"), "new runtime")
	writeFixture(t, filepath.Join(target, launcherExecutable()), "old executable")
	writeFixture(t, filepath.Join(target, "runtime.dat", "player.txt"), "player data")
	writeFixture(t, filepath.Join(target, "settings.json"), "player settings")
	if err := switchLauncher(staging, target, "2.36.7+client.0.1.0", component.Version, keys); err == nil || !strings.Contains(err.Error(), "previous version restored") {
		t.Fatalf("expected rollback after replacing executable, got %v", err)
	}
	assertContent(t, filepath.Join(target, launcherExecutable()), "old executable")
	assertContent(t, filepath.Join(target, "runtime.dat", "player.txt"), "player data")
	assertContent(t, filepath.Join(target, "settings.json"), "player settings")
	entries, err := filepath.Glob(filepath.Join(target, ".launcher-*.new"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary replacement files remain: %v, %v", entries, err)
	}
}

func TestSwitchLauncherIncludesUnchangedExecutableAndIgnoresUnlistedFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "installed")
	component := &release.Component{Version: "2.36.7+client.0.2.0", Files: []release.File{releaseFile(launcherExecutable(), "same executable"), releaseFile("runtime.dat", "new runtime")}}
	raw, keys := signedFixture(t, component, nil)
	m := New(Options{Directory: target})
	m.envelope, m.keys = raw, keys
	staging := m.launcherStaging(component.Version)
	writeFixture(t, filepath.Join(target, launcherExecutable()), "same executable")
	writeFixture(t, filepath.Join(target, "runtime.dat"), "old runtime")
	writeFixture(t, filepath.Join(target, "settings.json"), "player settings")
	writeFixture(t, filepath.Join(staging, "runtime.dat"), "new runtime")
	writeFixture(t, filepath.Join(staging, "settings.json"), "unexpected staged settings")
	if err := m.prepareLauncher(staging, component); err != nil {
		t.Fatal(err)
	}
	if err := switchLauncher(staging, target, "2.36.7+client.0.1.0", component.Version, keys); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(target, launcherExecutable()), "same executable")
	assertContent(t, filepath.Join(target, "runtime.dat"), "new runtime")
	assertContent(t, filepath.Join(target, "settings.json"), "player settings")
}
