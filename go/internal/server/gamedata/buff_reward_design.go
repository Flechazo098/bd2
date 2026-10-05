package gamedata

import "fmt"

// BuffItem is a permanent account-stat reward, not an inventory entity.
func LoadBuffRewardDesign(root, version string) (map[uint64]PictorialBuffStat, error) {
	pictorial, err := LoadPictorialDesign(root, version)
	if err != nil {
		return nil, err
	}
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := db.Query("SELECT ProtoBuf FROM BuffItemTable")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]PictorialBuffStat{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := optionalScalar(raw, 2)
		if err != nil {
			return nil, err
		}
		buffID, err := optionalScalar(raw, 1)
		if err != nil {
			return nil, err
		}
		buff, ok := pictorial.Buffs[buffID]
		if !ok || buff.Category != 1 {
			return nil, fmt.Errorf("gamedata: buff item %d permanent buff %d unavailable", id, buffID)
		}
		out[id] = PictorialBuffStat{Category: buff.Category, StatType: buff.StatType, Value: buff.Value}
	}
	return out, rows.Err()
}
