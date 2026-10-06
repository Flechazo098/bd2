package gamedata

import (
	"database/sql"
	"fmt"
)

// PackJamDesign is the installed client's insertion animation and reward rule.
type PackJamDesign struct {
	InsertMin, InsertMax uint64
	Reward               Reward
}

func LoadPackJamDesign(root, version string) (*PackJamDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadPackJamDesign(db)
}

func loadPackJamDesign(db *sql.DB) (*PackJamDesign, error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM PackJamEventTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var design *PackJamDesign
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if id != 0 || design != nil {
			return nil, fmt.Errorf("gamedata: unsupported pack jam rule %d", id)
		}
		values := map[int]uint64{}
		for field := 1; field <= 6; field++ {
			v, err := packedInts(raw, field)
			if err != nil || len(v) > 1 {
				return nil, fmt.Errorf("gamedata: invalid pack jam field %d", field)
			}
			if len(v) == 1 {
				values[field] = v[0]
			}
		}
		if values[1] != 0 || values[3] == 0 || values[2] < values[3] || values[2] > uint64(^uint32(0)>>1) || values[4] == 0 || values[4] > uint64(^uint32(0)>>1) || !packJamCurrency(values[6]) || values[5] != 0 {
			return nil, fmt.Errorf("gamedata: unsupported pack jam reward or insertion rule")
		}
		design = &PackJamDesign{InsertMin: values[3], InsertMax: values[2], Reward: Reward{Count: values[4], ID: values[5], Type: values[6]}}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if design == nil {
		return nil, fmt.Errorf("gamedata: missing pack jam rule")
	}
	return design, nil
}

func packJamCurrency(typ uint64) bool {
	switch typ {
	case 2, 3, 4, 12, 20:
		return true
	}
	return false
}

func (d PackJamDesign) ValidateReward() error {
	if !packJamCurrency(d.Reward.Type) || d.Reward.ID != 0 || d.Reward.Count == 0 || d.Reward.Count > uint64(^uint32(0)>>1) {
		return fmt.Errorf("gamedata: unsupported pack jam reward")
	}
	return nil
}
