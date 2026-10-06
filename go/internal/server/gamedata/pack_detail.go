package gamedata

import (
	"database/sql"
	"fmt"
)

// PackDetailDesign identifies the monsters whose state is consumed by the
// client's PackMapRewardInfo. Ordinary story battle rewards are excluded.
type PackDetailDesign struct{ RegenMonsterIDs []int }

func LoadPackDetailDesign(root, version string, packID int) (PackDetailDesign, error) {
	if packID <= 0 {
		return PackDetailDesign{}, fmt.Errorf("gamedata: invalid detail pack %d", packID)
	}
	db, release, err := OpenDatabase(root, version, fmt.Sprintf("pack%d", packID))
	if err != nil {
		return PackDetailDesign{}, err
	}
	defer release()
	return loadPackDetailDesign(db)
}

func loadPackDetailDesign(db *sql.DB) (PackDetailDesign, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM FieldMonsterTable ORDER BY id")
	if err != nil {
		return PackDetailDesign{}, err
	}
	defer func() { _ = rows.Close() }()
	var design PackDetailDesign
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return PackDetailDesign{}, err
		}
		values := map[int]uint64{}
		for _, field := range []int{24, 30, 31} {
			v, err := packedInts(raw, field)
			if err != nil || len(v) > 1 || id <= 0 {
				return PackDetailDesign{}, fmt.Errorf("gamedata: invalid detail monster %d field %d", id, field)
			}
			if len(v) == 1 {
				values[field] = v[0]
			}
		}
		if values[31] == 1 && values[30] != 3 && values[24] > 0 {
			design.RegenMonsterIDs = append(design.RegenMonsterIDs, id)
		}
	}
	return design, rows.Err()
}
