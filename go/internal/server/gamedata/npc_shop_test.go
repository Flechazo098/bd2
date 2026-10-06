package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestNPCShopCatalogKeepsCompositeProductKeysAndRejectsOrphans(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{"CREATE TABLE ShopTable(id INTEGER,ProtoBuf BLOB)", "CREATE TABLE ProductTable(id INTEGER,groupId INTEGER,ProtoBuf BLOB)", "CREATE TABLE SellItemTable(id INTEGER,ProtoBuf BLOB)"} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, shop := range []uint64{37, 48} {
		p := wire.AppendVarint(wire.AppendVarint(nil, 5, shop), 8, 91)
		if _, err = db.Exec("INSERT INTO ShopTable VALUES(?,?)", shop, p); err != nil {
			t.Fatal(err)
		}
		p = nil
		for _, f := range [][2]uint64{{7, 2}, {6, shop}, {3, 3}, {5, 5}, {4, 400}, {10, 25}, {12, 4}} {
			p = wire.AppendVarint(p, int(f[0]), f[1])
		}
		if _, err = db.Exec("INSERT INTO ProductTable VALUES(?,?,?)", 2, shop, p); err != nil {
			t.Fatal(err)
		}
	}
	d, err := loadNPCShopDesign(db)
	if err != nil || len(d.Products) != 2 || d.Products[37][2].Reward.Count != 3 || d.Products[48][2].Price.Count != 25 {
		t.Fatalf("lost composite product keys: %+v %v", d, err)
	}
	if _, err = db.Exec("DELETE FROM ShopTable WHERE id=48"); err != nil {
		t.Fatal(err)
	}
	if _, err = loadNPCShopDesign(db); err == nil {
		t.Fatal("accepted product whose shop is missing")
	}
}

func TestSellCatalogUsesTypesAndRejectsAmbiguousDefinitions(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{"CREATE TABLE ShopTable(id INTEGER,ProtoBuf BLOB)", "CREATE TABLE ProductTable(id INTEGER,groupId INTEGER,ProtoBuf BLOB)", "CREATE TABLE SellItemTable(id INTEGER,ProtoBuf BLOB)"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(id, typ, item uint64) {
		t.Helper()
		var p []byte
		for _, f := range [][2]uint64{{8, id}, {4, typ}, {3, item}, {2, 1}, {13, 4}, {11, 7}} {
			p = wire.AppendVarint(p, int(f[0]), f[1])
		}
		if _, err = db.Exec("INSERT INTO SellItemTable VALUES(?,?)", id, p); err != nil {
			t.Fatal(err)
		}
	}
	for i, typ := range []uint64{5, 8, 10} {
		insert(uint64(i+1), typ, 400)
	}
	d, err := loadNPCShopDesign(db)
	if err != nil || len(d.Sell) != 3 || !d.Sell[1].InventorySellable() || !d.Sell[2].InventorySellable() || d.Sell[3].InventorySellable() {
		t.Fatal("sell definition type boundaries", d, err)
	}
	insert(4, 8, 400)
	if _, err = loadNPCShopDesign(db); err == nil {
		t.Fatal("accepted ambiguous type/item price definition")
	}
}
