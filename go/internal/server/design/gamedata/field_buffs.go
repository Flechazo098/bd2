package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"math"
)

type FieldBuffDesign struct {
	ID, Type, TargetType uint64
	Value                float64
	Time                 float64
}

func optionalDouble(raw []byte, field int) (float64, error) {
	var value float64
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number == field {
			if f.Type != 1 {
				return fmt.Errorf("gamedata: invalid double field")
			}
			value = math.Float64frombits(binary.LittleEndian.Uint64(f.Value))
		}
		return nil
	})
	return value, e
}

func LoadFieldBuffDesign(root, version string) (map[uint64]FieldBuffDesign, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	rows, e := db.Query("SELECT id,ProtoBuf FROM FieldBuffTable ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := map[uint64]FieldBuffDesign{}
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		r := FieldBuffDesign{ID: id}
		for f, dst := range map[int]*uint64{6: &r.Type, 13: &r.TargetType} {
			v, x := optionalScalar(raw, f)
			if x != nil {
				return nil, x
			}
			*dst = v
		}
		v, x := optionalDouble(raw, 14)
		if x != nil {
			return nil, x
		}
		r.Value = v
		if r.Time, e = optionalDouble(raw, 5); e != nil {
			return nil, e
		}
		if id == 0 {
			return nil, fmt.Errorf("gamedata: invalid field buff")
		}
		out[id] = r
	}
	return out, rows.Err()
}
