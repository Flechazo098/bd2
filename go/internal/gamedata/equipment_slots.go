package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// LoadEquipmentSlots reads EquipmentTable.SlotType for every installed item.
// SlotType is a proto3 enum, so an absent field is the valid first slot (0).
func LoadEquipmentSlots(root, version string) (map[uint64]uint64, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-equipment-slots-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadEquipmentSlots(db)
}

func loadEquipmentSlots(db *sql.DB) (map[uint64]uint64, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM EquipmentTable")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
