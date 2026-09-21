package clientplugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallRequiresBepInExWithoutCopyingPlugin(t *testing.T) {
	gameDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gameDir, "BrownDust II.exe"), []byte("game"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(source, []byte("plugin"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Install(gameDir, source)
	if err == nil || !strings.Contains(err.Error(), BepInExReleasesURL) {
		t.Fatalf("missing BepInEx error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(gameDir, "BepInEx", "plugins", FileName)); !os.IsNotExist(statErr) {
		t.Fatalf("plugin was copied without BepInEx: %v", statErr)
	}
}

func TestInstallCopiesUpdatesAndSkipsIdenticalPlugin(t *testing.T) {
	gameDir := t.TempDir()
	for path, data := range map[string][]byte{
		filepath.Join(gameDir, "BrownDust II.exe"):               []byte("game"),
		filepath.Join(gameDir, "BepInEx", "core", "BepInEx.dll"): []byte("bepinex"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(source, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Install(gameDir, source)
	if err != nil || !first.Changed {
		t.Fatalf("first install=%+v err=%v", first, err)
	}
	second, err := Install(gameDir, source)
	if err != nil || second.Changed {
		t.Fatalf("idempotent install=%+v err=%v", second, err)
	}
	if err := os.WriteFile(source, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := Install(gameDir, source)
	if err != nil || !third.Changed {
		t.Fatalf("update=%+v err=%v", third, err)
	}
	got, err := os.ReadFile(third.Destination)
	if err != nil || string(got) != "v2" {
		t.Fatalf("installed=%q err=%v", got, err)
	}
}
