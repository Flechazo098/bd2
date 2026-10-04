package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// PackDetailDesign identifies the monsters whose state is consumed by the
// client's PackMapRewardInfo. Ordinary story battle rewards are excluded.
type PackDetailDesign struct{ RegenMonsterIDs []int }

func LoadPackDetailDesign(root, version string, packID int) (PackDetailDesign, error) {
	if packID <= 0 {
		return PackDetailDesign{}, fmt.Errorf("gamedata: invalid detail pack %d", packID)
	}
	plain, err := ReadDatabase(root, version, fmt.Sprintf("pack%d", packID))
	if err != nil {
		return PackDetailDesign{}, err
	}
	dir, err := os.MkdirTemp("", "bd2-pack-detail-")
	if err != nil {
		return PackDetailDesign{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "pack.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return PackDetailDesign{}, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return PackDetailDesign{}, err
	}
	defer db.Close()
	return loadPackDetailDesign(db)
}

func loadPackDetailDesign(db *sql.DB) (PackDetailDesign, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM FieldMonsterTable ORDER BY id")
	if err != nil {
		return PackDetailDesign{}, err
	}
	defer rows.Close()
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
