package gamedata

import (
	"database/sql"
	"fmt"
)

// FieldPack describes non-story arena fields. PackType comes from the protocol
// enum; IDs and entry restrictions come from the installed GameData tables.
type FieldPack struct {
	ID, Type, BuyPrice, UseSchedule uint64
	TicketID, SquadLevel            uint64
	HasOpenRule                     bool
	MapIDs                          map[int]bool
}

func LoadFieldPacks(root, version string) (map[int]FieldPack, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadFieldPacks(db)
}

func loadFieldPacks(db *sql.DB) (map[int]FieldPack, error) {
	result := map[int]FieldPack{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM PackTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		values := map[int]uint64{}
		for _, field := range []int{7, 25, 55, 65} {
			v, e := packedInts(raw, field)
			if e != nil || len(v) > 1 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid arena pack %d field %d", id, field)
			}
			if len(v) == 1 {
				values[field] = v[0]
			}
		}
		if values[55] != 3 && values[55] != 5 && values[55] != 10 {
			continue
		}
		if id <= 0 || values[25] != uint64(id) {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: invalid field pack identity %d", id)
		}
		result[id] = FieldPack{ID: uint64(id), Type: values[55], BuyPrice: values[7], UseSchedule: values[65], MapIDs: map[int]bool{}}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM ContentOpenTable WHERE groupId=1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pack, exists := result[id]
		if !exists {
			continue
		}
		pack.HasOpenRule = true
		for field, target := range map[int]*uint64{5: &pack.SquadLevel, 6: &pack.TicketID} {
			v, e := packedInts(raw, field)
			if e != nil || len(v) > 1 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid field pack opening %d", id)
			}
			if len(v) == 1 {
				*target = v[0]
			}
		}
		result[id] = pack
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,packId FROM MapTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, packID int
		if err := rows.Scan(&id, &packID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if pack, exists := result[packID]; exists {
			pack.MapIDs[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for id, pack := range result {
		if len(pack.MapIDs) == 0 {
			return nil, fmt.Errorf("gamedata: arena pack %d has no map", id)
		}
	}
	return result, nil
}
