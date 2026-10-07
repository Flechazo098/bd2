package gamedata

import (
	"database/sql"
	"fmt"
)

// LoadPackSummaryTargets follows PackMapRewardInfo.IsSummaryTargetPackType in
// the 2.35.10 client. Pack identities and types come from the installed tables.
func LoadPackSummaryTargets(root, version string) (map[int]bool, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadPackSummaryTargets(db)
}

func loadPackSummaryTargets(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM PackTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[int]bool{}
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		types, err := packedInts(raw, 55)
		if err != nil || len(types) > 1 || id <= 0 {
			return nil, fmt.Errorf("gamedata: invalid summary pack %d", id)
		}
		var packType uint64
		if len(types) == 1 {
			packType = types[0]
		}
		switch packType {
		case 2, 3, 5, 7, 8, 9, 10, 12, 13, 14, 100:
			continue
		}
		result[id] = true
	}
	return result, rows.Err()
}
