package gamedata

import (
	"os"
	"testing"
)

func TestRewardCostumeCatalogInstalledSmoke(t *testing.T) {
	root := os.Getenv("BD2_GAMEDATA_ROOT")
	version := os.Getenv("BD2_GAMEDATA_VERSION")
	if root == "" || version == "" {
		t.Skip("explicit installed GameData integration environment required")
	}
	d, e := LoadRewardCostumeCatalog(root, version)
	if e != nil {
		t.Fatal(e)
	}
	if len(d.characters) == 0 {
		t.Fatal("empty catalog")
	}
	if _, ok := d.Character(60601); !ok {
		t.Fatal("installed event costume omitted")
	}
}
