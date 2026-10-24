//go:build integration

package gamedata_test

import (
	"slices"
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
)

// Pack 3003's scene data puts research objects 2..6 on Map3003_015..019,
// which MapTable does not list. They must not stop the pack's reachable
// research, such as object 1005 on Map3003_003 (map 30032), from loading.
func TestPack3003ResearchLoadsDespiteUnlistedScenes(t *testing.T) {
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	d, err := gamedata.LoadFieldResearch(versions.Resolve("data/resources/GameData"), versions.GameDataVersion, 3003)
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := d.Objects[1005]; !ok || !slices.Contains(o.Maps, 30032) {
		t.Fatalf("research 1005 = %+v (present=%t), want it on map 30032", o, ok)
	}
}
