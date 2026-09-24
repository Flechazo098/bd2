package gamedata

import (
	"database/sql"
	"testing"

	"bd2server/internal/wire"
	_ "modernc.org/sqlite"
)

func TestLoadInventorySlotDesignUsesGameDefaultFields(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE GameDefaultTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for field, value := range map[int]uint64{
		9: 300, 10: 4, 11: 300, 12: 4, 13: 300, 14: 4, 15: 300, 16: 4,
		30: 500, 31: 100, 34: 100, 37: 100, 73: 2000, 74: 400, 79: 10000, 80: 500, 83: 400,
	} {
		raw = wire.AppendVarint(raw, field, value)
	}
	if _, err := db.Exec("INSERT INTO GameDefaultTable(id,ProtoBuf) VALUES(0,?)", raw); err != nil {
		t.Fatal(err)
	}
	design, err := loadInventorySlotDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	if design.Items.Default != 100 || design.Items.Maximum != 500 || design.Equipment.Default != 500 || design.Equipment.Maximum != 2000 || design.Storage.Maximum != 400 || design.EquipmentStorage.Maximum != 400 {
		t.Fatalf("design=%+v", design)
	}
}
