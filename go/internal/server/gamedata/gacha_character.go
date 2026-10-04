package gamedata

import (
	"database/sql"
	"fmt"
)

// Costume ownership refers to a unique character, not a numeric costume ID
// prefix. The initial growth grade is the base character, excluding pack-only
// temporary copies; ambiguous rows are rejected instead of selecting an ID.
func loadCostumeBaseCharacterID(db *sql.DB, costumeID uint64) (uint64, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CostumeTable WHERE id=?", costumeID).Scan(&raw); err != nil {
		return 0, err
	}
	unique, err := optionalScalar(raw, 27)
	if err != nil || unique == 0 {
		return 0, fmt.Errorf("gamedata: costume %d unique character unavailable", costumeID)
	}
	rows, err := db.Query("SELECT id,ProtoBuf FROM CharTable WHERE uniqueCharId=?", unique)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var result uint64
	for rows.Next() {
		var id uint64
		var row []byte
		if err := rows.Scan(&id, &row); err != nil {
			return 0, err
		}
		growth, err := optionalScalar(row, 10)
		if err != nil {
			return 0, err
		}
		temporary, err := optionalScalar(row, 21)
		if err != nil {
			return 0, err
		}
		if growth != 1 || temporary != 0 {
			continue
		}
		if result != 0 {
			return 0, fmt.Errorf("gamedata: costume %d has ambiguous base character", costumeID)
		}
		result = id
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if result == 0 {
		return 0, fmt.Errorf("gamedata: costume %d has no base character", costumeID)
	}
	return result, nil
}
