package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallSetReplacesOnlyTheManagedPlugins(t *testing.T) {
	root := t.TempDir()
	write := func(relative, value string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	read := func(relative string) (string, error) {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		return string(raw), err
	}
	game := filepath.Join(root, "game")
	write("game/BrownDust II.exe", "fixture")
	write("game/BrownDust II_Data/resources.assets", "fixture")
	write("game/BepInEx/core/BepInEx.dll", "fixture")
	write("game/BepInEx/plugins/OtherMod.dll", "player's own plugin")
	write("game/BepInEx/config/BD2FChat.cfg", "player settings")

	first := []string{write("v1/BD2LoginUI.dll", "login v1"), write("v1/BD2FChat.dll", "chat v1")}
	result, err := InstallSet(game, first)
	if err != nil || !result.Changed {
		t.Fatalf("first install = %+v, %v", result, err)
	}

	// The next set drops FChat and changes LoginUI.
	second := []string{write("v2/BD2LoginUI.dll", "login v2")}
	result, err = InstallSet(game, second)
	if err != nil || !result.Changed {
		t.Fatalf("second install = %+v, %v", result, err)
	}
	if got, _ := read("game/BepInEx/plugins/BD2LoginUI.dll"); got != "login v2" {
		t.Errorf("LoginUI = %q, want the new set's file", got)
	}
	if _, err := read("game/BepInEx/plugins/BD2FChat.dll"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a plugin dropped from the managed set was left installed: %v", err)
	}
	if got, _ := read("game/BepInEx/plugins/OtherMod.dll"); got != "player's own plugin" {
		t.Errorf("an unmanaged plugin was touched: %q", got)
	}
	if got, _ := read("game/BepInEx/config/BD2FChat.cfg"); got != "player settings" {
		t.Errorf("plugin configuration was touched: %q", got)
	}

	result, err = InstallSet(game, second)
	if err != nil || result.Changed {
		t.Fatalf("reinstalling the same set = %+v, %v; want no change", result, err)
	}
}

func TestInstalledDetectsAnOutdatedPlugin(t *testing.T) {
	root := t.TempDir()
	write := func(relative, value string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	game := filepath.Join(root, "game")
	write("game/BrownDust II.exe", "fixture")
	write("game/BrownDust II_Data/resources.assets", "fixture")
	write("game/BepInEx/plugins/BD2LoginUI.dll", "login v1")
	newer := []string{write("store/BD2LoginUI.dll", "login v2")}
	if installed, err := Installed(game, newer); err != nil || installed {
		t.Fatalf("Installed with an older DLL = %v, %v; want false so the launcher installs the set", installed, err)
	}
	missing := []string{write("store/BD2FChat.dll", "chat")}
	if installed, err := Installed(game, missing); err != nil || installed {
		t.Fatalf("Installed with a missing DLL = %v, %v; want false", installed, err)
	}
	write("game/BepInEx/plugins/BD2LoginUI.dll", "login v2")
	if installed, err := Installed(game, newer); err != nil || !installed {
		t.Fatalf("Installed with matching DLL = %v, %v; want true", installed, err)
	}
}

func pluginFixture(t *testing.T) (string, func(string, string) string) {
	t.Helper()
	root := t.TempDir()
	write := func(relative, value string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("game/BrownDust II.exe", "fixture")
	write("game/BrownDust II_Data/resources.assets", "fixture")
	write("game/BepInEx/core/BepInEx.dll", "fixture")
	write("game/BepInEx/plugins/OtherMod.dll", "player plugin")
	write("game/BepInEx/config/BD2FChat.cfg", "player config")
	write("game/BepInEx/plugins/lang/custom.json", "player language")
	return root, write
}

func assertPluginFiles(t *testing.T, root string, want map[string]string) {
	t.Helper()
	for name, expected := range want {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || string(raw) != expected {
			t.Errorf("%s = %q, %v; want %q", name, raw, err, expected)
		}
	}
}

func TestInstallSetRollsBackWhenDroppedPluginCannotBeRemoved(t *testing.T) {
	root, write := pluginFixture(t)
	game := filepath.Join(root, "game")
	first := []string{write("v1/BD2LoginUI.dll", "old login"), write("v1/BD2FChat.dll", "old chat")}
	if _, err := InstallSet(game, first); err != nil {
		t.Fatal(err)
	}
	chat := filepath.Join(game, "BepInEx", "plugins", "BD2FChat.dll")
	if err := os.Remove(chat); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(chat, 0o700); err != nil {
		t.Fatal(err)
	}
	write("game/BepInEx/plugins/BD2FChat.dll/player.txt", "do not remove")
	next := []string{write("v2/BD2LoginUI.dll", "new login"), write("v2/BD2CashShop.dll", "new shop")}
	if _, err := InstallSet(game, next); err == nil {
		t.Fatal("install succeeded despite an undeletable managed entry")
	}
	assertPluginFiles(t, root, map[string]string{
		"game/BepInEx/plugins/BD2LoginUI.dll":          "old login",
		"game/BepInEx/plugins/BD2FChat.dll/player.txt": "do not remove",
		"game/BepInEx/plugins/OtherMod.dll":            "player plugin",
		"game/BepInEx/config/BD2FChat.cfg":             "player config",
		"game/BepInEx/plugins/lang/custom.json":        "player language",
	})
	if _, err := os.Stat(filepath.Join(game, "BepInEx", "plugins", "BD2CashShop.dll")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new plugin survived rollback: %v", err)
	}
	names, err := readManaged(filepath.Join(game, "BepInEx", "plugins"))
	if err != nil || len(names) != 2 || names[1] != "BD2FChat.dll" {
		t.Fatalf("managed ownership changed: %v, %v", names, err)
	}
}

func TestInstallSetRollsBackEveryChangeWhenMetadataIsLocked(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows denies replacing open metadata files")
	}
	root, write := pluginFixture(t)
	game := filepath.Join(root, "game")
	first := []string{write("v1/BD2LoginUI.dll", "old login"), write("v1/BD2FChat.dll", "old chat")}
	if _, err := InstallSet(game, first); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(game, "BepInEx", "plugins", managedFile)
	old, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.Open(metadata)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	next := []string{write("v2/BD2LoginUI.dll", "new login"), write("v2/BD2CashShop.dll", "new shop")}
	if _, err := InstallSet(game, next); err == nil || !strings.Contains(err.Error(), "record managed plugins") {
		t.Fatalf("want failure after DLL replacement and removal, got %v", err)
	}
	assertPluginFiles(t, root, map[string]string{
		"game/BepInEx/plugins/BD2LoginUI.dll":   "old login",
		"game/BepInEx/plugins/BD2FChat.dll":     "old chat",
		"game/BepInEx/plugins/" + managedFile:   string(old),
		"game/BepInEx/plugins/OtherMod.dll":     "player plugin",
		"game/BepInEx/config/BD2FChat.cfg":      "player config",
		"game/BepInEx/plugins/lang/custom.json": "player language",
	})
	if _, err := os.Stat(filepath.Join(game, "BepInEx", "plugins", "BD2CashShop.dll")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new plugin survived rollback: %v", err)
	}
}

func TestInstallSetRejectsOwnershipThatEscapesThePluginDirectory(t *testing.T) {
	root, write := pluginFixture(t)
	game := filepath.Join(root, "game")
	write("game/BepInEx/plugins/.bd2-managed.json", `["../config/player.dll"]`)
	write("game/BepInEx/config/player.dll", "player data")
	source := write("v2/BD2LoginUI.dll", "new login")
	if _, err := InstallSet(game, []string{source}); err == nil {
		t.Fatal("accepted ownership outside plugins")
	}
	assertPluginFiles(t, root, map[string]string{"game/BepInEx/config/player.dll": "player data"})
	if _, err := os.Stat(filepath.Join(game, "BepInEx", "plugins", "BD2LoginUI.dll")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed DLL despite invalid ownership: %v", err)
	}
}
