package gamedata

import "fmt"

// GameplayAchievementGrades resolves subtype rarity from the current tables.
// Instances contain design IDs, never inferred numeric ID ranges.
type GameplayAchievementGrades struct {
	Characters, Equipment, Costumes   map[uint64]uint64
	CharacterGrowth, EquipmentPrivate map[uint64]uint64
	CostumeMaximum                    map[uint64]uint64
}

func LoadGameplayAchievementGrades(root, version string) (GameplayAchievementGrades, error) {
	d := GameplayAchievementGrades{Characters: map[uint64]uint64{}, Equipment: map[uint64]uint64{}, Costumes: map[uint64]uint64{}, CharacterGrowth: map[uint64]uint64{}, EquipmentPrivate: map[uint64]uint64{}, CostumeMaximum: map[uint64]uint64{}}
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
			field := 10
			target := d.CharacterGrowth
			if spec.table == "EquipmentTable" {
				field, target = 16, d.EquipmentPrivate
			}
			value, err := optionalScalar(raw, field)
			if err != nil {
				_ = rows.Close()
				return d, err
			}
			target[id] = value
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return d, err
		}
	}
	rows, err := db.Query("SELECT ProtoBuf FROM CharTable")
	if err != nil {
		return d, err
	}
	uniqueGrades := map[uint64]uint64{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return d, err
		}
		kind, err := optionalScalar(raw, 19)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		if kind != 0 {
			continue
		}
		unique, err := optionalScalar(raw, 20)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		grade, err := optionalScalar(raw, 9)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		if previous, found := uniqueGrades[unique]; found && previous != grade {
			_ = rows.Close()
			return d, fmt.Errorf("gamedata: character rarity mismatch")
		}
		uniqueGrades[unique] = grade
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return d, err
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM CostumeTable")
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
		unique, err := optionalScalar(raw, 27)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		d.Costumes[id] = uniqueGrades[unique]
		maximum, err := optionalScalar(raw, 15)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		d.CostumeMaximum[id] = maximum
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return d, err
	}
	return d, nil
}
