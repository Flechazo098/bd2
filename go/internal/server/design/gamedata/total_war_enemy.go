package gamedata

import "fmt"

type TotalWarEnemy struct{ ID, Level, Grid uint64 }

func loadTotalWarEnemies(root, version string) ([]TotalWarEnemy, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	var pack uint64
	err = castleRows(db, "PackTable", func(id uint64, raw []byte) error {
		typ, err := optionalScalar(raw, 55)
		if err != nil {
			return err
		}
		if typ == 7 {
			if pack != 0 {
				return fmt.Errorf("gamedata: multiple total war packs")
			}
			pack = id
		}
		return nil
	})
	done()
	if err != nil || pack == 0 {
		return nil, fmt.Errorf("gamedata: total war pack unavailable: %v", err)
	}
	db, done, err = openPackDatabase(root, version, int(pack))
	if err != nil {
		return nil, err
	}
	defer done()
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=1").Scan(&raw); err != nil {
		return nil, err
	}
	ids, err := packedInts(raw, 14)
	if err != nil {
		return nil, err
	}
	levels, err := packedInts(raw, 30)
	if err != nil {
		return nil, err
	}
	positions, err := packedInts(raw, 32)
	if err != nil || len(ids) == 0 || len(ids) != len(levels) || len(ids) != len(positions) {
		return nil, fmt.Errorf("gamedata: invalid total war enemies")
	}
	var out []TotalWarEnemy
	for i, id := range ids {
		if id == 0 || levels[i] == 0 {
			return nil, fmt.Errorf("gamedata: invalid total war enemy")
		}
		out = append(out, TotalWarEnemy{ID: id, Level: levels[i], Grid: positions[i]})
	}
	return out, nil
}
