package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallRequiresBepInExWithoutCopyingPlugin(t *testing.T) {
	for _, spec := range []Spec{LocalIdentity, LoginUI} {
		t.Run(spec.FileName(), func(t *testing.T) {
			gameDir := t.TempDir()
			for path, data := range map[string][]byte{
				filepath.Join(gameDir, "BrownDust II.exe"):                      []byte("game"),
				filepath.Join(gameDir, "BrownDust II_Data", "resources.assets"): []byte("assets"),
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(t.TempDir(), spec.FileName())
			if err := os.WriteFile(filepath.Join(filepath.Dir(source), GameNames.FileName()), []byte("names-v1"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("plugin"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Install(spec, gameDir, source)
			if err == nil || !strings.Contains(err.Error(), BepInExReleasesURL) {
				t.Fatalf("missing BepInEx error=%v", err)
			}
			if _, statErr := os.Stat(filepath.Join(gameDir, "BepInEx", "plugins", spec.FileName())); !os.IsNotExist(statErr) {
				t.Fatalf("plugin was copied without BepInEx: %v", statErr)
			}
		})
	}
}

func TestInstallCopiesUpdatesAndSkipsIdenticalPlugin(t *testing.T) {
	for _, spec := range []Spec{LocalIdentity, LoginUI} {
		t.Run(spec.FileName(), func(t *testing.T) {
			gameDir := t.TempDir()
			for path, data := range map[string][]byte{
				filepath.Join(gameDir, "BrownDust II.exe"):                      []byte("game"),
				filepath.Join(gameDir, "BrownDust II_Data", "resources.assets"): []byte("assets"),
				filepath.Join(gameDir, "BepInEx", "core", "BepInEx.dll"):        []byte("bepinex"),
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(t.TempDir(), spec.FileName())
			if err := os.WriteFile(filepath.Join(filepath.Dir(source), GameNames.FileName()), []byte("names-v1"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("v1"), 0o600); err != nil {
				t.Fatal(err)
			}
			first, err := Install(spec, gameDir, source)
			if err != nil || !first.Changed {
				t.Fatalf("first install=%+v err=%v", first, err)
			}
			second, err := Install(spec, gameDir, source)
			if err != nil || second.Changed {
				t.Fatalf("idempotent install=%+v err=%v", second, err)
			}
			runtimeSource := filepath.Join(filepath.Dir(source), GameNames.FileName())
			if err := os.WriteFile(runtimeSource, []byte("names-v2"), 0o600); err != nil {
				t.Fatal(err)
			}
			runtimeUpdate, err := Install(spec, gameDir, source)
			if err != nil || !runtimeUpdate.Changed {
				t.Fatalf("runtime-only update=%+v err=%v", runtimeUpdate, err)
			}
			installedRuntime, err := os.ReadFile(filepath.Join(gameDir, "BepInEx", "plugins", GameNames.FileName()))
			if err != nil || string(installedRuntime) != "names-v2" {
				t.Fatalf("installed runtime=%q err=%v", installedRuntime, err)
			}
			if err := os.WriteFile(source, []byte("v2"), 0o600); err != nil {
				t.Fatal(err)
			}
			third, err := Install(spec, gameDir, source)
			if err != nil || !third.Changed {
				t.Fatalf("update=%+v err=%v", third, err)
			}
			got, err := os.ReadFile(third.Destination)
			if err != nil || string(got) != "v2" {
				t.Fatalf("installed=%q err=%v", got, err)
			}
		})
	}
}

func TestInstallMissingSharedRuntimeDoesNotChangePlugin(t *testing.T) {
	gameDir := t.TempDir()
	for path, data := range map[string][]byte{
		filepath.Join(gameDir, "BrownDust II.exe"):                             []byte("game"),
		filepath.Join(gameDir, "BrownDust II_Data", "resources.assets"):        []byte("assets"),
		filepath.Join(gameDir, "BepInEx", "core", "BepInEx.dll"):               []byte("bepinex"),
		filepath.Join(gameDir, "BepInEx", "plugins", LocalIdentity.FileName()): []byte("old-plugin"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(t.TempDir(), LocalIdentity.FileName())
	if err := os.WriteFile(source, []byte("new-plugin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(LocalIdentity, gameDir, source); err == nil || !strings.Contains(err.Error(), GameNames.FileName()) {
		t.Fatalf("missing runtime error=%v", err)
	}
	installed, err := os.ReadFile(filepath.Join(gameDir, "BepInEx", "plugins", LocalIdentity.FileName()))
	if err != nil || string(installed) != "old-plugin" {
		t.Fatalf("installed=%q err=%v", installed, err)
	}
}

func TestInstallKeepsPluginsSeparate(t *testing.T) {
	gameDir := t.TempDir()
	for path, data := range map[string][]byte{
		filepath.Join(gameDir, "BrownDust II.exe"):                      []byte("game"),
		filepath.Join(gameDir, "BrownDust II_Data", "resources.assets"): []byte("assets"),
		filepath.Join(gameDir, "BepInEx", "core", "BepInEx.dll"):        []byte("bepinex"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []Spec{LocalIdentity, LoginUI} {
		source := filepath.Join(t.TempDir(), spec.FileName())
		if err := os.WriteFile(filepath.Join(filepath.Dir(source), GameNames.FileName()), []byte("names-v1"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, []byte(spec.FileName()), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(spec, gameDir, source); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []Spec{LocalIdentity, LoginUI} {
		path := filepath.Join(gameDir, "BepInEx", "plugins", spec.FileName())
		data, err := os.ReadFile(path)
		if err != nil || string(data) != spec.FileName() {
			t.Fatalf("%s=%q err=%v", spec.FileName(), data, err)
		}
	}
}
