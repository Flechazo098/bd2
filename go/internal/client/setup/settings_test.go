package setup

import (
	"os"
	"path/filepath"
	"testing"

	clientconfig "bd2server/internal/client/config"
)

func TestSaveLocalSettingsBeforeGameAndResourcesAreInstalled(t *testing.T) {
	root := t.TempDir()
	game, resources := filepath.Join(root, "game"), filepath.Join(root, "resources")
	input := clientconfig.Settings{ServerOrigin: "https://play.example.com", CDNMode: clientconfig.CDNLocal, LocalResourceDirectory: resources}
	if _, err := SaveSettings(game, input); err != nil {
		t.Fatal("saving an installation location depended on game or resource preparation", err)
	}
	saved, err := clientconfig.Load(game)
	if err != nil || saved.ServerOrigin != input.ServerOrigin || saved.CDNMode != input.CDNMode || saved.LocalResourceDirectory != resources {
		t.Fatalf("saved settings could not be restored for preparation: %+v, %v", saved, err)
	}
	if _, err := os.Stat(resources); !os.IsNotExist(err) {
		t.Fatal("saving settings tried to create the resource installation", err)
	}
	if _, err := os.Stat(filepath.Join(game, "BrownDust II_Data")); !os.IsNotExist(err) {
		t.Fatal("saving settings tried to prepare the game", err)
	}
	configPath := clientconfig.Path(game)
	before, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSettings(game, input); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(configPath)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("saving unchanged settings rewrote the configuration", err)
	}
}
