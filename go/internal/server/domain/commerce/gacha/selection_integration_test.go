//go:build integration

package gacha

import (
	"slices"
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/roster"
)

// GameData 20261021182514: group 10001 -> Gacha 100/101 -> RewardGroup
// 100/101 -> five-star group 70011 contains costume 806. The live rejected
// request on 2026-10-17 used group=10001, slot=0, item=806.
func TestCurrentGameDataTwelvePickAcceptsReportedCostume(t *testing.T) {
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			t.Error(err)
		}
	})
	catalog, err := gamedata.LoadRegularCostumeGachaGroups(versions.Resolve("data/resources/GameData"), versions.GameDataVersion, []uint64{10001, 1007}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, ctx := selectionService(t)
	service.regular = catalog
	// Explicit independently queried targets, with the reported rejection first.
	ids := []uint64{806, 101, 102, 103, 104, 105, 106, 201, 202, 203, 204, 301}
	var choices []roster.GachaSelection
	for i, id := range ids {
		choices = append(choices, roster.GachaSelection{GroupID: 10001, Slot: uint64(i), ItemID: id})
	}
	code, _, handled, err := service.Handle(ctx, "/GachaSelectionSave", selectionRequest(choices))
	if err != nil || !handled || code != 198 {
		t.Fatalf("current 12PICK selection rejected: code=%d handled=%v error=%v", code, handled, err)
	}
	if got := selectionInfo(t, service, ctx); !slices.Equal(got, choices) {
		t.Fatalf("12PICK response targets=%v, want %v", got, choices)
	}
	// Composite reward programs must retain their selectable five-star pool.
	if got := catalog.FiveStarIDs(30); !slices.Contains(got, 101) {
		t.Fatalf("special selection reward program lost costume 101: %v", got)
	}
}
