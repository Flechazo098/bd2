package gamedata

import (
	"fmt"
	"math"
)

type ItemStackDesign struct {
	Limits map[[2]uint64]uint64
}

func LoadItemStackDesign(root, version string) (*ItemStackDesign, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	design := &ItemStackDesign{Limits: map[[2]uint64]uint64{}}
	for _, source := range []struct {
		name                      string
		kind, idField, limitField uint64
	}{
		{"FoodTable", 5, 7, 16}, {"ResourceTable", 8, 4, 12},
		{"RandomBoxTable", 9, 4, 12}, {"UseItemTable", 14, 3, 10},
	} {
		rows, err := db.Query("SELECT id,ProtoBuf FROM " + source.name + " ORDER BY id")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id uint64
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				_ = rows.Close()
				return nil, err
			}
			protoID, err := optionalScalar(raw, int(source.idField))
			if err != nil || id == 0 || protoID != id {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid %s stack identity %d", source.name, id)
			}
			limit, err := optionalScalar(raw, int(source.limitField))
			if err != nil || limit > math.MaxInt32 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid %s stack limit %d", source.name, id)
			}
			if limit > 0 {
				design.Limits[[2]uint64{source.kind, id}] = limit
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return design, nil
}
