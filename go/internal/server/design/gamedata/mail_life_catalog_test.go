package gamedata

import (
	"database/sql"
	"testing"

	"bd2server/internal/server/protocol/wire"
)

func TestMailCatalogIncludesLifeCurrencyObjectsAndAllItemKinds(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, table := range []string{"NameTextTable", "RandomBoxTextTable", "LocalTextTable", "ResourceTable", "FoodTable", "CookingTable", "RandomBoxTable", "QuestItemTable", "UseItemTable", "CollectionTable", "MyRoomItemTable", "InstantUseItemTable", "LifeObjectGroupTable", "LifeItemTable", "CurrencyTable", "RewardGroupTable", "CashProductTable"} {
		if _, err := db.Exec(`CREATE TABLE "` + table + `" (id INTEGER PRIMARY KEY, ProtoBuf BLOB NOT NULL)`); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(values ...uint64) []byte {
		var b []byte
		for i := 0; i < len(values); i += 2 {
			b = wire.AppendVarint(b, int(values[i]), values[i+1])
		}
		return b
	}
	insert := func(table string, id uint64, b []byte) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO "`+table+`" (id, ProtoBuf) VALUES (?, ?)`, id, b); err != nil {
			t.Fatal(err)
		}
	}
	// Names and field numbers follow the 2.37.13 LifeItemTable,
	// LifeObjectGroupTable and CurrencyTable generated Proto definitions.
	for id, name := range map[uint64]string{1: "Oak plank", 2: "Carrot", 3: "Stew", 4: "Chair", 5: "Workshop", 6: "Garden statue", 7: "Life coins"} {
		insert("NameTextTable", id, wire.AppendString(encode(2, id), 4, name))
	}
	insert("LifeItemTable", 101, encode(5, 101, 7, 1, 9, 1))
	insert("LifeItemTable", 102, encode(5, 102, 7, 2, 9, 2))
	insert("LifeItemTable", 103, encode(5, 103, 7, 3, 9, 3))
	insert("LifeObjectGroupTable", 201, encode(5, 201, 11, 4, 12, 1))
	insert("LifeObjectGroupTable", 202, encode(5, 202, 11, 5, 12, 3))
	insert("LifeObjectGroupTable", 203, encode(5, 203, 11, 6, 12, 4))
	insert("CurrencyTable", 64, encode(3, 64, 5, 7))
	items, err := loadMailCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]uint64][2]string{
		{64, 0}:   {"Life coins", "currency"},
		{65, 201}: {"Chair", "life_furniture"},
		{65, 202}: {"Workshop", "life_building"},
		{65, 203}: {"Garden statue", "life_decoration"},
		{66, 101}: {"Oak plank", "life_material"},
		{66, 102}: {"Carrot", "life_product"},
		{66, 103}: {"Stew", "life_cooking"},
	}
	if len(items) != len(want) {
		t.Fatalf("catalog contains %d entries, want %d: %+v", len(items), len(want), items)
	}
	for _, item := range items {
		key := [2]uint64{item.ElementType, item.ID}
		expected, ok := want[key]
		if !ok || item.Name != expected[0] || item.Category != expected[1] {
			t.Fatalf("unexpected catalog entry: %+v", item)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing life entries: %v", want)
	}
}
