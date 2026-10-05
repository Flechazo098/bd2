package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

// WaypointPack uses the per-pack FieldWaypointTable and common PackTable.
// A zero PriceUnit is a real proto3 default and means free travel.
type WaypointPack struct {
	Points               map[uint64]Waypoint
	PriceType, PriceUnit uint64
}
type Waypoint struct {
	ID, MapID  uint64
	SafeArea   bool
	QuestRange []uint64
}

func LoadWaypointPack(root, version string, packID uint64) (WaypointPack, error) {
	if packID == 0 || packID > math.MaxInt32 {
		return WaypointPack{}, fmt.Errorf("gamedata: invalid waypoint pack")
	}
	common, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return WaypointPack{}, err
	}
	defer closeDB()
	var raw []byte
	if err = common.QueryRow("SELECT ProtoBuf FROM PackTable WHERE id=?", packID).Scan(&raw); err != nil {
		return WaypointPack{}, err
	}
	design := WaypointPack{Points: map[uint64]Waypoint{}}
	for field, target := range map[int]*uint64{66: &design.PriceType, 67: &design.PriceUnit} {
		values, e := packedInts(raw, field)
		if e != nil || len(values) > 1 {
			return WaypointPack{}, fmt.Errorf("gamedata: invalid waypoint price pack%d", packID)
		}
		if len(values) == 1 {
			*target = values[0]
		}
	}
	db, release, err := OpenDatabase(root, version, fmt.Sprintf("pack%d", packID))
	if err != nil {
		return WaypointPack{}, err
	}
	defer release()
	design.Points, err = loadWaypointPoints(db)
	return design, err
}

func loadWaypointPoints(db *sql.DB) (map[uint64]Waypoint, error) {
	points := map[uint64]Waypoint{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM FieldWaypointTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uint64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		p := Waypoint{ID: id}
		if id == 0 || id > math.MaxInt32 {
			return nil, fmt.Errorf("gamedata: invalid waypoint id")
		}
		for field, target := range map[int]*uint64{2: &p.ID, 5: &p.MapID} {
			values, e := packedInts(raw, field)
			if e != nil || len(values) > 1 {
				return nil, fmt.Errorf("gamedata: invalid waypoint field")
			}
			if len(values) == 1 {
				*target = values[0]
			}
		}
		if p.ID != id || p.MapID > math.MaxInt32 {
			return nil, fmt.Errorf("gamedata: invalid waypoint identity")
		}
		safe, e := packedInts(raw, 4)
		if e != nil || len(safe) > 1 {
			return nil, fmt.Errorf("gamedata: invalid waypoint safe area")
		}
		p.SafeArea = len(safe) == 1 && safe[0] != 0
		p.QuestRange, e = packedInts(raw, 6)
		if e != nil {
			return nil, e
		}
		points[id] = p
	}
	return points, rows.Err()
}
