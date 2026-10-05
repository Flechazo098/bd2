package commerce

import (
	"os"
	"testing"

	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/gamedata"
)

// Opt in against the installed release; synthetic tests also run without it.
func TestInstalledCashCatalogAndRewardPrograms(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not set")
	}
	design, err := gamedata.LoadCashCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog("2.35.10", design, gameconfig.Default().Purchases)
	if err != nil {
		t.Fatal(err)
	}
	rewards, err := gamedata.LoadCashRewardResolver(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	entitlements, err := gamedata.LoadCashEntitlementDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	counts, recharge := 0, 0
	for _, product := range design.Products {
		if product.PriceType != 1 {
			continue
		}
		counts++
		if product.Recharge {
			recharge++
		}
		quote, err := catalog.Quote(product.Key, 1)
		if err != nil {
			t.Fatalf("quote %+v: %v", product.Key, err)
		}
		if product.Recharge && (quote.ItemType != 0 || quote.Amount != 0) {
			t.Fatalf("default recharge isn't free: %+v", quote)
		}
		if !product.Recharge && (quote.ItemType != 2 || quote.Amount == 0) {
			t.Fatalf("cash goods aren't paid diamond: %+v", quote)
		}
		// Infinite preview products use the gacha delegate, whose selected
		// costumes replace weighted/empty placeholder reward programs.
		if product.Key.GroupID == 1100001 {
			continue
		}
		leaves, err := rewards.ResolveGranted([]gamedata.BattleReward{{Type: 9, ID: product.RandomBoxID, Count: 1}})
		if err != nil || len(leaves) == 0 {
			t.Fatalf("product %+v: no reward program: %v", product.Key, err)
		}
		for _, leaf := range leaves {
			if leaf.Type == 19 && entitlements.TicketTypes[leaf.ID] == 2 && len(entitlements.Attendance[leaf.ID]) == 0 {
				t.Fatalf("monthly reward schedule missing: %d", leaf.ID)
			}
			if leaf.Type == 62 && !entitlements.AvatarSets[leaf.ID] {
				t.Fatalf("avatar set missing: %d", leaf.ID)
			}
		}
	}
	if counts != 614 || recharge != 14 {
		t.Fatalf("unexpected cash coverage: goods=%d recharge=%d", counts, recharge)
	}
	for _, key := range []gamedata.CashProductKey{{GroupID: 1100001, ProductID: 9100033}, {GroupID: 310001, ProductID: 507}, {GroupID: 310001, ProductID: 518}} {
		quote, err := catalog.Quote(key, 1)
		if err != nil || quote.Amount != 1000 || quote.ItemType != 2 {
			t.Fatalf("sample %+v: %+v %v", key, quote, err)
		}
	}
	t.Logf("Verified all %d cash products, %d recharge variants and %d platform aliases", counts, recharge, len(catalog.Manifest().Products))
}
