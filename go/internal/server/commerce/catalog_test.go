package commerce

import (
	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/gamedata"
	"math"
	"testing"
)

func testDesign() *gamedata.CashCatalog {
	return &gamedata.CashCatalog{Products: []gamedata.CashProductDesign{
		{Key: gamedata.CashProductKey{GroupID: 1, ProductID: 1, SaleGroup: 1}, GoogleSKU: "recharge", AppleSKU: "recharge-ios", PriceType: 1, PriceCount: 2200, Recharge: true, NominalPaidDiamonds: 100},
		{Key: gamedata.CashProductKey{GroupID: 1, ProductID: 2, SaleGroup: 1}, GoogleSKU: "recharge", AppleSKU: "recharge-ios", PriceType: 1, PriceCount: 2200, Recharge: true, NominalPaidDiamonds: 100},
		{Key: gamedata.CashProductKey{GroupID: 2, ProductID: 1, SaleGroup: 1}, GoogleSKU: "skin", AppleSKU: "skin-ios", PriceType: 1, PriceCount: 5501},
		{Key: gamedata.CashProductKey{GroupID: 3, ProductID: 1}, GoogleSKU: "native", PriceType: 2, PriceCount: 100},
	}}
}
func TestCashPricesAliasesAndIsolation(t *testing.T) {
	for _, currency := range []string{"free", "gold", "diamonds", "ban", ""} {
		cfg := gameconfig.Default().Purchases
		cfg.DiamondRecharge.Currency = currency
		c, err := NewCatalog("test", testDesign(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		m := c.Manifest()
		if m.SchemaVersion != 1 || len(m.Products) != 6 {
			t.Fatalf("manifest %+v", m)
		}
		skin, err := c.Quote(gamedata.CashProductKey{GroupID: 2, ProductID: 1, SaleGroup: 1}, 2)
		if err != nil || skin.Cost != 502 || skin.ItemType != 2 || skin.Currency != "paid_diamonds" {
			t.Fatalf("ordinary goods affected by %q: %+v %v", currency, skin, err)
		}
		recharge, err := c.Quote(gamedata.CashProductKey{GroupID: 1, ProductID: 1, SaleGroup: 1}, 1)
		if currency == "ban" || currency == "" {
			if err == nil {
				t.Fatal("disabled recharge accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want := uint64(0)
		if currency == "gold" {
			want = 100000
		}
		if currency == "diamonds" {
			want = 100
		}
		if recharge.Cost != want {
			t.Fatalf("currency %s cost=%d", currency, recharge.Cost)
		}
		second, _ := c.Quote(gamedata.CashProductKey{GroupID: 1, ProductID: 2, SaleGroup: 1}, 1)
		if second.Cost != recharge.Cost {
			t.Fatal("first purchase incorrectly doubled cost")
		}
		m.Products[0].SKU = "changed"
		if c.Manifest().Products[0].SKU == "changed" {
			t.Fatal("manifest mutable through caller")
		}
		if _, err = c.Quote(gamedata.CashProductKey{GroupID: 3, ProductID: 1}, 1); err == nil {
			t.Fatal("native wallet item intercepted")
		}
	}
}
func TestMonetaryBoundsAndBaseline(t *testing.T) {
	for _, v := range [][3]uint64{{math.MaxUint64, 2, 1}, {1, 0, 1}, {1, 1, 0}, {math.MaxInt32 + 1, 1, 1}} {
		if _, err := ceilRatio(v[0], v[1], v[2]); err == nil {
			t.Fatalf("accepted %v", v)
		}
	}
	d := testDesign()
	d.Products[0].PriceCount = 2000
	d.Products[1].PriceCount = 2000
	c, err := NewCatalog("test", d, gameconfig.Default().Purchases)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c.Quote(d.Products[2].Key, 1)
	if p.Cost != 276 {
		t.Fatalf("baseline not derived dynamically: %+v", p)
	}
	if _, err = c.Quote(d.Products[2].Key, math.MaxInt32); err == nil {
		t.Fatal("overflow accepted")
	}
	if _, err = c.Quote(d.Products[2].Key, 0); err == nil {
		t.Fatal("zero purchase accepted")
	}
	d.Products[0].Recharge = false
	d.Products[1].Recharge = false
	if _, err = NewCatalog("test", d, gameconfig.Default().Purchases); err == nil {
		t.Fatal("missing baseline accepted")
	}
}
