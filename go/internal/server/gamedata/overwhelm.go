package gamedata

import (
	"fmt"
)

type SkyWayOverwhelmRule struct {
	Group, Map, APType, Boss, BossAP uint64
	Monsters, AP                     []uint64
}

func LoadSkyWayOverwhelm(root, version string) ([]SkyWayOverwhelmRule, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	rows, e := db.Query("SELECT ProtoBuf FROM SkyWayFieldTable")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	var out []SkyWayOverwhelmRule
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		r := SkyWayOverwhelmRule{}
		for f, p := range map[int]*uint64{10: &r.Group, 13: &r.Map, 1: &r.APType, 4: &r.Boss, 3: &r.BossAP} {
			*p, e = optionalScalar(b, f)
			if e != nil {
				return nil, e
			}
		}
		r.Monsters, e = packedInts(b, 15)
		if e != nil {
			return nil, e
		}
		r.AP, e = packedInts(b, 14)
		if e != nil {
			return nil, e
		}
		if len(r.Monsters) != len(r.AP) {
			return nil, fmt.Errorf("gamedata: malformed skyway AP mapping")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type OverwhelmQuestRule struct {
	Type, Count uint64
	Targets     []uint64
	Enemies     map[uint64][]uint64
}

func LoadOverwhelmQuest(root, version string, pack, quest int) (OverwhelmQuestRule, error) {
	r := OverwhelmQuestRule{Enemies: map[uint64][]uint64{}}
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return r, e
	}
	defer done()
	var b []byte
	if e = db.QueryRow(fmt.Sprintf("SELECT ProtoBuf FROM QuestTable%d WHERE id=?", pack), quest).Scan(&b); e != nil {
		return r, e
	}
	r.Type, e = optionalScalar(b, 11)
	if e != nil {
		return r, e
	}
	r.Count, e = optionalScalar(b, 10)
	if e != nil {
		return r, e
	}
	r.Targets, e = packedInts(b, 33)
	if e != nil {
		return r, e
	}
	packdb, release, e := openPackDatabase(root, version, pack)
	if e != nil {
		return r, e
	}
	defer release()
	rows, e := packdb.Query("SELECT id,ProtoBuf FROM BattleDeckTable")
	if e != nil {
		return r, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			return r, e
		}
		ids, e := packedInts(raw, 14)
		if e != nil {
			return r, e
		}
		r.Enemies[id] = ids
	}
	return r, rows.Err()
}
