package gamedata

import "fmt"

func loadFieldStatues(root, version string, pack int) (map[uint64]Reward, error) {
	db, done, err := openPackDatabase(root, version, pack)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := db.Query("SELECT id FROM FieldStatueObjectTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for rows.Next() {
		var id uint64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	out := map[uint64]Reward{}
	if len(ids) == 0 {
		return out, nil
	}
	common, closeCommon, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeCommon()
	for _, id := range ids {
		var raw []byte
		if err = common.QueryRow("SELECT ProtoBuf FROM StatueRewardTable WHERE id=?", id).Scan(&raw); err != nil {
			return nil, err
		}
		v := make([]uint64, 4)
		for i := range v {
			v[i], err = optionalScalar(raw, i+1)
			if err != nil {
				return nil, err
			}
		}
		if v[0] != id || v[1] == 0 || v[3] == 0 {
			return nil, fmt.Errorf("gamedata: invalid statue reward %d", id)
		}
		out[id] = Reward{Type: v[3], ID: v[2], Count: v[1]}
	}
	return out, nil
}
