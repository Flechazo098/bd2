package gamedata

import (
	"database/sql"
	"testing"
)

func TestLoadEquipmentSlotsKeepsProtoDefaultSlotZero(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE EquipmentTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO EquipmentTable VALUES (?,?),(?,?)", 10010, []byte{}, 943619, testVarintField(nil, 20, 4)); err != nil {
		t.Fatal(err)
	}
	slots, err := loadEquipmentSlots(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 || slots[10010] != 0 || slots[943619] != 4 {
		t.Fatalf("slots=%v", slots)
	}
}
