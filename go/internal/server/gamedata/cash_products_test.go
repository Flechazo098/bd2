package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestCashCatalogPreservesKeysAliasesAndLimits(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	db.SetMaxOpenConns(1)
	for _, q := range []string{"CREATE TABLE CashProductTable(groupId INTEGER,id INTEGER,saleGroup INTEGER,ProtoBuf BLOB)", "CREATE TABLE RandomBoxTable(id INTEGER,ProtoBuf BLOB)", "CREATE TABLE RewardGroupTable(id INTEGER,ProtoBuf BLOB)", "CREATE TABLE CashShopTable(ProtoBuf BLOB)", "CREATE TABLE CashPackageTable(ProtoBuf BLOB)", "CREATE TABLE EventShopTable(ProtoBuf BLOB)"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i, count := range []uint64{200, 100} {
		raw := wire.AppendVarint(nil, 2, 1)
		raw = wire.AppendVarint(raw, 6, 2)
		raw = wire.AppendVarint(raw, 5, 0)
		raw = wire.AppendVarint(raw, 4, count)
		raw = wire.AppendVarint(raw, 8, 100)
		if _, err = db.Exec("INSERT INTO RewardGroupTable VALUES(?,?)", i+1, raw); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT INTO RandomBoxTable VALUES(?,?)", i+1, wire.AppendVarint(nil, 9, uint64(i+1))); err != nil {
			t.Fatal(err)
		}
		raw = wire.AppendString(nil, 4, "shared-sku")
		raw = wire.AppendString(raw, 1, "shared-ios")
		for _, f := range []struct {
			n int
			v uint64
		}{{5, 99}, {6, uint64(i + 1)}, {15, 7}, {9, 1}, {7, 2200}, {14, uint64(i + 1)}, {2, 333}, {12, 1}, {13, 3}, {16, 2}, {3, 1}} {
			raw = wire.AppendVarint(raw, f.n, f.v)
		}
		if _, err = db.Exec("INSERT INTO CashProductTable VALUES(99,?,7,?)", i+1, raw); err != nil {
			t.Fatal(err)
		}
	}
	shop := wire.AppendVarint(nil, 4, 8)
	shop = wire.AppendVarint(shop, 5, 1)
	shop = wire.AppendVarint(shop, 10, 99)
	if _, err = db.Exec("INSERT INTO CashShopTable VALUES(?)", shop); err != nil {
		t.Fatal(err)
	}
	packageRaw := wire.AppendVarint(nil, 8, 99)
	packageRaw = wire.AppendVarint(packageRaw, 9, 1)
	packageRaw = wire.AppendVarint(packageRaw, 17, 7)
	packageRaw = wire.AppendVarint(packageRaw, 11, 12)
	if _, err = db.Exec("INSERT INTO CashPackageTable VALUES(?)", packageRaw); err != nil {
		t.Fatal(err)
	}
	c, err := loadCashCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Products) != 2 || len(c.Shops) != 1 || len(c.Packages) != 1 {
		t.Fatalf("incomplete %+v", c)
	}
	for _, p := range c.Products {
		if !p.Recharge || p.NominalPaidDiamonds != 100 || p.GoogleSKU != "shared-sku" || p.AppleSKU != "shared-ios" || p.BonusRandomBoxID != 333 || p.PurchaseLimitType != 3 || p.TimeLimitType != 2 {
			t.Fatalf("lost metadata %+v", p)
		}
	}
}
func TestDecodeCashProductRejectsMalformedMonetaryFields(t *testing.T) {
	for _, raw := range [][]byte{{255}, wire.AppendVarint(nil, 5, 1), wire.AppendVarint(wire.AppendVarint(nil, 5, 1), 6, 1<<40)} {
		if _, err := decodeCashProduct(raw); err == nil {
			t.Fatalf("accepted %x", raw)
		}
	}
}
