package gamedata

import (
	"database/sql"
	"fmt"
)

// Costume ownership refers to a unique character, not a numeric costume ID
// prefix. The initial growth grade is the base character, excluding pack-only
// temporary copies; ambiguous rows are rejected instead of selecting an ID.
func loadCostumeBaseCharacterID(db *sql.DB, costumeID uint64) (uint64, error) {
	base, _, err := loadCostumeCharacterFamily(db, costumeID)
	return base, err
}

// The family includes every permanent promotion stage. A costume reward must
// reuse its owned character even when promotion has changed CharTable.Id.
func loadCostumeCharacterFamily(db *sql.DB, costumeID uint64) (uint64, []uint64, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CostumeTable WHERE id=?", costumeID).Scan(&raw); err != nil {
		return 0, nil, err
	}
	unique, err := optionalScalar(raw, 27)
	if err != nil || unique == 0 {
		return 0, nil, fmt.Errorf("gamedata: costume %d unique character unavailable", costumeID)
	}
	rows, err := db.Query("SELECT id,ProtoBuf FROM CharTable WHERE uniqueCharId=?", unique)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = rows.Close() }()
	var result uint64
	var family []uint64
	for rows.Next() {
		var id uint64
		var row []byte
		if err := rows.Scan(&id, &row); err != nil {
			return 0, nil, err
		}
		growth, err := optionalScalar(row, 10)
		if err != nil {
			return 0, nil, err
		}
		temporary, err := optionalScalar(row, 21)
		if err != nil {
			return 0, nil, err
		}
		kind, err := optionalScalar(row, 19)
		if err != nil {
			return 0, nil, err
		}
		if temporary != 0 || kind != 0 {
			continue
		}
		family = append(family, id)
		if growth != 1 {
			continue
		}
		if result != 0 {
			return 0, nil, fmt.Errorf("gamedata: costume %d has ambiguous base character", costumeID)
		}
		result = id
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if result == 0 {
		return 0, nil, fmt.Errorf("gamedata: costume %d has no base character", costumeID)
	}
	return result, family, nil
}
