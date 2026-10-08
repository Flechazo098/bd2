package gamedata

import "fmt"

type SkyWayStage struct {
	Group, ID, Difficulty, Map, Point, APType, Boss, BossAP, PositionGroup uint64
	Monsters, AP                                                           []uint64
}

type SkyWayDesign struct {
	Pack     int
	Stages   []SkyWayStage
	Monsters map[uint64]HuntingMonster
	SafeMaps map[int]bool
}

func LoadSkyWayDesign(root, version string) (*SkyWayDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &SkyWayDesign{Monsters: map[uint64]HuntingMonster{}, SafeMaps: map[int]bool{}}
	rows, err := db.Query("SELECT ProtoBuf FROM SkyWayFieldTable ORDER BY groupId,difficulty")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var s SkyWayStage
		for f, p := range map[int]*uint64{1: &s.APType, 3: &s.BossAP, 4: &s.Boss, 6: &s.Difficulty, 10: &s.Group, 12: &s.ID, 13: &s.Map, 17: &s.Point, 18: &s.PositionGroup} {
			*p, err = optionalScalar(raw, f)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
		}
		s.Monsters, err = packedInts(raw, 15)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		s.AP, err = packedInts(raw, 14)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		if s.Group == 0 || s.Group > 7 || s.ID == 0 || s.Boss == 0 || s.Map == 0 || len(s.AP) != len(s.Monsters) || (s.APType != 1 && s.APType != 2) {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: invalid SkyWay stage")
		}
		d.Stages = append(d.Stages, s)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for _, s := range d.Stages {
		var pack int
		if err = db.QueryRow("SELECT packId FROM MapTable WHERE id=?", s.Map).Scan(&pack); err != nil {
			return nil, err
		}
		if d.Pack != 0 && d.Pack != pack {
			return nil, fmt.Errorf("gamedata: SkyWay maps span packs")
		}
		d.Pack = pack
	}
	if d.Pack == 0 {
		return nil, fmt.Errorf("gamedata: no SkyWay pack")
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM MapTable WHERE packId=?", d.Pack)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		typ, e := optionalScalar(raw, 22)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		if typ == 0 {
			d.SafeMaps[id] = true
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	pdb, release, err := openPackDatabase(root, version, d.Pack)
	if err != nil {
		return nil, err
	}
	defer release()
	for _, s := range d.Stages {
		for _, id := range append(append([]uint64(nil), s.Monsters...), s.Boss) {
			if _, ok := d.Monsters[id]; ok {
				continue
			}
			var raw []byte
			if err = pdb.QueryRow("SELECT ProtoBuf FROM FieldMonsterTable WHERE id=?", id).Scan(&raw); err != nil {
				return nil, err
			}
			m := HuntingMonster{ID: id, Rewards: map[uint64][]BattleReward{}}
			m.Type, err = optionalScalar(raw, 30)
			if err != nil {
				return nil, err
			}
			m.Decks, err = packedInts(raw, 2)
			if err != nil || len(m.Decks) == 0 {
				return nil, fmt.Errorf("gamedata: SkyWay monster missing decks")
			}
			for _, deck := range m.Decks {
				if err = pdb.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", deck).Scan(&raw); err != nil {
					return nil, err
				}
				rs, e := parallelRewards(raw, 36, 34, 33)
				if e != nil {
					return nil, e
				}
				for _, r := range rs {
					m.Rewards[deck] = append(m.Rewards[deck], BattleReward(r))
				}
			}
			d.Monsters[id] = m
		}
	}
	return d, nil
}

func (d *SkyWayDesign) Stage(group, id uint64) (SkyWayStage, bool) {
	for _, s := range d.Stages {
		if s.Group == group && s.ID == id {
			return s, true
		}
	}
	return SkyWayStage{}, false
}

// Battle modes are the current protocol's contiguous SKY_WAY enum family.
func SkyWayMode(group uint64) uint64 { return 8 + group }
func IsSkyWayMode(mode uint64) bool  { return mode >= 9 && mode <= 15 }
