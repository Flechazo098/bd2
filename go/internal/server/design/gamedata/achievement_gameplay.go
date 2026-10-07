package gamedata

import "fmt"

// GameplayAchievementGrades resolves subtype rarity from the current tables.
// Instances contain design IDs, never inferred numeric ID ranges.
type GameplayAchievementGrades struct{ Characters, Equipment map[uint64]uint64 }

func LoadGameplayAchievementGrades(root, version string) (GameplayAchievementGrades, error) {
	d := GameplayAchievementGrades{Characters: map[uint64]uint64{}, Equipment: map[uint64]uint64{}}
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return d, err
	}
	defer closeDB()
	for _, spec := range []struct {
		table  string
		field  int
		target map[uint64]uint64
	}{{"CharTable", 9, d.Characters}, {"EquipmentTable", 3, d.Equipment}} {
		rows, err := db.Query("SELECT id,ProtoBuf FROM " + spec.table)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var id uint64
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				_ = rows.Close()
				return d, err
			}
			grade, err := packedInts(raw, spec.field)
			if err != nil || len(grade) != 1 || grade[0] == 0 {
				_ = rows.Close()
				return d, fmt.Errorf("gamedata: invalid achievement grade %s/%d", spec.table, id)
			}
			spec.target[id] = grade[0]
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return d, err
		}
	}
	return d, nil
}
