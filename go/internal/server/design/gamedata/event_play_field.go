package gamedata

import "fmt"

type EventField struct {
	HP, Limit    uint64
	Objects      map[uint64]FieldGameObject
	RandomCounts map[uint64]uint64
}

type FieldGameObject struct {
	Point             int64
	Type, RandomGroup uint64
}

func (c *EventPlayCatalog) Field(game uint64) (*EventField, error) {
	g, e := c.Row("PackEventMiniGameTable", 8, game)
	if e != nil {
		return nil, e
	}
	pack, _ := optionalScalar(g, 12)
	target, _ := optionalScalar(g, 4)
	db, done, e := openPackDatabase(c.root, c.version, int(pack))
	if e != nil {
		return nil, e
	}
	defer done()
	var raw []byte
	if e = db.QueryRow("SELECT ProtoBuf FROM FieldMiniGameTable WHERE id=?", target).Scan(&raw); e != nil {
		return nil, e
	}
	group, _ := optionalScalar(raw, 3)
	d := &EventField{Objects: map[uint64]FieldGameObject{}, RandomCounts: map[uint64]uint64{}}
	d.HP, _ = optionalScalar(raw, 6)
	d.Limit, _ = optionalScalar(raw, 7)
	counts, e := packedInts(raw, 4)
	if e != nil {
		return nil, e
	}
	groups, e := packedInts(raw, 5)
	if e != nil {
		return nil, e
	}
	if len(counts) != len(groups) {
		return nil, fmt.Errorf("gamedata: field random group counts differ")
	}
	for i, group := range groups {
		if group == 0 {
			return nil, fmt.Errorf("gamedata: invalid field random group")
		}
		if _, exists := d.RandomCounts[group]; exists {
			return nil, fmt.Errorf("gamedata: duplicate field random group")
		}
		d.RandomCounts[group] = counts[i]
	}
	rows, e := db.Query("SELECT ProtoBuf FROM FieldMiniGameObjectTable WHERE groupId=? ORDER BY id", group)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		id, _ := optionalScalar(raw, 2)
		point, _ := optionalScalar(raw, 3)
		typ, _ := optionalScalar(raw, 4)
		randomGroup, _ := optionalScalar(raw, 5)
		if id == 0 || typ < 1 || typ > 3 {
			return nil, fmt.Errorf("gamedata: invalid field object")
		}
		d.Objects[id] = FieldGameObject{Point: int64(int32(point)), Type: typ, RandomGroup: randomGroup}
	}
	if len(d.Objects) == 0 {
		return nil, fmt.Errorf("gamedata: field game objects missing")
	}
	return d, rows.Err()
}
