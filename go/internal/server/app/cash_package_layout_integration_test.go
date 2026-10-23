//go:build integration

package app

import (
	"fmt"
	"strings"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

// Current official PackageUI148.prefab uses BundleGroupPackageUI, whose
// per-product buttons only check availability and that product's own limit.
// Its contentsGroup91 sort3 product236 can be bought before sort2 product235.
// PackageUI132 uses BundleGroupRelayPackageUI and does enforce stage order.
func TestIndependentCashPackageCanBeBoughtBeforeEarlierDisplayItems(t *testing.T) {
	g := profileGameplay(t)
	profileGrant(g, gamedata.Reward{Type: 2, Count: 1000000})
	before := g.owner.assembly.wallet.Snapshot(command.Context{}).Jewelry
	key := gamedata.CashProductKey{GroupID: 310001, ProductID: 236}
	quote, err := g.owner.assembly.cashCatalog.Quote(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	line := wire.AppendVarint(wire.AppendVarint(nil, 1, key.ProductID), 3, 1)
	pay := "98236"
	billing := wire.AppendString(wire.AppendString(nil, 1, pay), 2, fmt.Sprintf("bd2-local-commerce-v1:%s:%d:%d", pay, quote.ItemType, quote.Cost))
	request := wire.AppendBytes(wire.AppendBytes(wire.AppendVarint(nil, 3, key.GroupID), 4, line), 7, billing)
	g.call("/CashShopBuy", request)
	assertPurchase := func() {
		t.Helper()
		if got := g.owner.assembly.wallet.Snapshot(command.Context{}).Jewelry; got != before-quote.Cost {
			t.Fatalf("independent package did not debit its accepted quote once: got %d", got)
		}
		var bought, prior uint64
		for _, row := range appRows(g.call("/CashShopPurchaseCountInfo", nil), 1) {
			if appValue(row, 1) == 310001 && appValue(row, 3) == 0 {
				switch appValue(row, 2) {
				case 236:
					bought = appValue(row, 4)
				case 235:
					prior = appValue(row, 4)
				}
			}
		}
		if bought != 1 || prior != 0 {
			t.Fatalf("purchase counts changed another display tier: bought=%d prior=%d", bought, prior)
		}
	}
	assertPurchase()
	g.reopen()
	assertPurchase()
	// A real relay's later stage remains unavailable; no quote or debit is
	// attempted until its predecessor is owned.
	var relay gamedata.CashPackageDesign
	for _, pack := range g.factory.design.cashDesign.Packages {
		if pack.ResourceName == "PackageUI132" && pack.ContentsSortID == 2 {
			relay = pack
			break
		}
	}
	if relay.ID == 0 {
		t.Fatal("official relay fixture absent")
	}
	relayLine := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, relay.ID), 2, relay.SaleGroup), 3, 1)
	failure := g.execute(g.request("/CashShopBuy", wire.AppendBytes(wire.AppendVarint(nil, 3, relay.GroupID), 4, relayLine)), true)
	if !strings.Contains(string(failure), "preceding package purchase required") {
		t.Fatalf("real relay order was not enforced: %s", failure)
	}
	assertPurchase()
}
