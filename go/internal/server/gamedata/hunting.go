package gamedata

import "fmt"

type HuntingMonster struct {
	ID, Type uint64
	Decks    []uint64
	Rewards  map[uint64][]BattleReward
}
type HuntingGround struct {
	ID, MapID, Difficulty, BossID uint64
	Monsters                      []uint64
}
type HuntingPack struct {
	Grounds          []HuntingGround
	Monsters         map[uint64]HuntingMonster
	NormalAP, BossAP uint64
}

func LoadHuntingPack(root, version string, pack int) (*HuntingPack, error) {
	db, cleanup, err := openPackDatabase(root, version, pack)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	common, closeCommon, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeCommon()
	d := &HuntingPack{Monsters: map[uint64]HuntingMonster{}}
	var defaults []byte
	if err := common.QueryRow("SELECT ProtoBuf FROM GameDefaultTable").Scan(&defaults); err != nil {
		return nil, err
	}
	d.BossAP, err = optionalScalar(defaults, 121)
	if err != nil {
		return nil, err
	}
	d.NormalAP, err = optionalScalar(defaults, 122)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT ProtoBuf FROM HuntingGroundTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		g := HuntingGround{}
		for field, target := range map[int]*uint64{1: &g.BossID, 2: &g.Difficulty, 3: &g.ID, 4: &g.MapID} {
			v, e := optionalScalar(raw, field)
			if e != nil {
				rows.Close()
				return nil, e
			}
			*target = v
		}
		g.Monsters, err = packedInts(raw, 5)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if g.ID == 0 || g.MapID == 0 || g.BossID == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid hunting ground")
		}
		d.Grounds = append(d.Grounds, g)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, g := range d.Grounds {
		ids := append(append([]uint64(nil), g.Monsters...), g.BossID)
		for _, id := range ids {
			if _, ok := d.Monsters[id]; ok {
				continue
			}
			var raw []byte
			if err := db.QueryRow("SELECT ProtoBuf FROM FieldMonsterTable WHERE id=?", id).Scan(&raw); err != nil {
				return nil, err
			}
			m := HuntingMonster{ID: id, Rewards: map[uint64][]BattleReward{}}
			m.Type, err = optionalScalar(raw, 30)
			if err != nil {
				return nil, err
			}
			m.Decks, err = packedInts(raw, 2)
			if err != nil || len(m.Decks) == 0 {
				return nil, fmt.Errorf("gamedata: missing hunting monster deck")
			}
			for _, deck := range m.Decks {
				var b []byte
				if err := db.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", deck).Scan(&b); err != nil {
					return nil, err
				}
				r, e := parallelRewards(b, 36, 34, 33)
				if e != nil {
					return nil, e
				}
				types, e := packedInts(b, 36)
				if e != nil {
					return nil, e
				}
				if len(r) != len(types) {
					return nil, fmt.Errorf("gamedata: hunting reward arrays mismatch")
				}
				for _, reward := range r {
					m.Rewards[deck] = append(m.Rewards[deck], BattleReward{reward.Type, reward.ID, reward.Count})
				}
			}
			d.Monsters[id] = m
		}
	}
	return d, nil
}
