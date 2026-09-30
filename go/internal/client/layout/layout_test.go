package layout

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestResolveWindows(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "BrownDust II.exe"))
	writeFile(t, filepath.Join(root, "BrownDust II_Data", "resources.assets"))
	got, err := Resolve(root)
	if err != nil || got.Kind != Windows || got.Resources != filepath.Join(root, "BrownDust II_Data", "resources.assets") || got.Plugins != filepath.Join(root, "BepInEx", "plugins") {
		t.Fatalf("layout=%+v err=%v", got, err)
	}
}

func TestResolveMacAppAndParent(t *testing.T) {
	parent := t.TempDir()
	app := filepath.Join(parent, "BrownDust II.app")
	writeFile(t, filepath.Join(app, "Contents", "MacOS", "BrownDust II"))
	writeFile(t, filepath.Join(app, "Contents", "Resources", "Data", "resources.assets"))
	for _, selected := range []string{app, parent} {
		got, err := Resolve(selected)
		if err != nil || got.Kind != MacOS || got.Root != app || got.BepInEx != filepath.Join(parent, "BepInEx") || got.LaunchTarget() != app {
			t.Fatalf("selected=%q layout=%+v err=%v", selected, got, err)
		}
	}
}
