package gamedata

import "fmt"

type EventField struct {
	HP, Limit uint64
	Objects   map[uint64]struct{ Point, Type uint64 }
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
	d := &EventField{Objects: map[uint64]struct{ Point, Type uint64 }{}}
	d.HP, _ = optionalScalar(raw, 6)
	d.Limit, _ = optionalScalar(raw, 7)
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
		d.Objects[id] = struct{ Point, Type uint64 }{point, typ}
	}
	if len(d.Objects) == 0 {
		return nil, fmt.Errorf("gamedata: field game objects missing")
	}
	return d, rows.Err()
}
