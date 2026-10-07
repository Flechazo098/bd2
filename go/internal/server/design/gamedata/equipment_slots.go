package gamedata

import (
	"database/sql"
	"fmt"
)

// LoadEquipmentSlots reads EquipmentTable.SlotType for every installed item.
// SlotType is a proto3 enum, so an absent field is the valid first slot (0).
func LoadEquipmentSlots(root, version string) (map[uint64]uint64, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	return loadEquipmentSlots(db)
}

func loadEquipmentSlots(db *sql.DB) (map[uint64]uint64, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM EquipmentTable")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	slots := make(map[uint64]uint64)
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			return nil, err
		}
		values, err := packedInts(proto, 20)
		if err != nil || len(values) > 1 {
			return nil, fmt.Errorf("gamedata: equipment %d malformed slot: %v", id, err)
		}
		var slot uint64
		if len(values) == 1 {
			slot = values[0]
		}
		if slot > 4 {
			return nil, fmt.Errorf("gamedata: equipment %d invalid slot %d", id, slot)
		}
		slots[id] = slot
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(slots) == 0 {
		return nil, fmt.Errorf("gamedata: EquipmentTable is empty")
	}
	return slots, nil
}
