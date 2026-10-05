package gamedata

import "fmt"

type NPCReputationRule struct{ ID, DownHours, GoodInn, GoodPrice uint64 }
type NPCReputationDesign struct {
	Groups    map[uint64]NPCReputationRule
	MapGroups map[int]uint64
}

func LoadNPCReputation(root, version string, pack int) (NPCReputationDesign, error) {
	d := NPCReputationDesign{Groups: map[uint64]NPCReputationRule{}, MapGroups: map[int]uint64{}}
	db, done, err := openPackDatabase(root, version, pack)
	if err != nil {
		return d, err
	}
	defer done()
	rows, err := db.Query("SELECT id,ProtoBuf FROM ReputationGroupTable")
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return d, err
		}
		r := NPCReputationRule{ID: id}
		for f, p := range map[int]*uint64{1: &r.DownHours, 2: &r.GoodInn, 3: &r.GoodPrice} {
			*p, err = optionalScalar(raw, f)
			if err != nil {
				rows.Close()
				return d, err
			}
		}
		if id == 0 || r.GoodInn > 100 || r.GoodPrice > 100 || r.DownHours > 596523 {
			rows.Close()
			return d, fmt.Errorf("gamedata: invalid reputation rule")
		}
		d.Groups[id] = r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return d, err
	}
	shared, release, err := openStatDatabase(root, version)
	if err != nil {
		return d, err
	}
	defer release()
	rows, err = shared.Query("SELECT id,ProtoBuf FROM MapTable")
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return d, err
		}
		p, e := optionalScalar(raw, 28)
		if e != nil {
			return d, e
		}
		if p != uint64(pack) {
			continue
		}
		group, e := optionalScalar(raw, 18)
		if e != nil {
			return d, e
		}
		d.MapGroups[id] = group
	}
	return d, rows.Err()
}
