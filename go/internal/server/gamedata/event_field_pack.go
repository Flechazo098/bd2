package gamedata

import (
	"database/sql"
	"fmt"
)

// EventFieldPack combines installed hidden-pack rules with a current calendar
// identity. Calendar times use milliseconds, as do event schedule protocols.
type EventFieldPack struct {
	ID                            int
	ScheduleUID, GameID, HubID    uint64
	MapIDs                        []int
	InitialPosition               string
	InitialMapID, PointPositionID uint64
	BuyType, BuyPrice             uint64
	ContentOpenType               uint64
	BuyRewards                    []Reward
	End                           int64
}

func loadEventFieldPacks(db *sql.DB) (map[int]EventFieldPack, error) {
	packs := map[int]EventFieldPack{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM PackTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		kind, e := optionalScalar(raw, 55)
		if e != nil {
			rows.Close()
			return nil, e
		}
		if kind != 100 {
			continue
		}
		protoID, e := optionalScalar(raw, 25)
		if e != nil || protoID != uint64(id) || id <= 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid hidden pack %d", id)
		}
		p := EventFieldPack{ID: id, InitialPosition: "{}"}
		if p.BuyType, err = optionalScalar(raw, 11); err != nil {
			rows.Close()
			return nil, err
		}
		if p.BuyPrice, err = optionalScalar(raw, 7); err != nil {
			rows.Close()
			return nil, err
		}
		rewards, e := eventGameRewards(raw, 10, 9, 8)
		if e != nil {
			rows.Close()
			return nil, e
		}
		for _, r := range rewards {
			p.BuyRewards = append(p.BuyRewards, Reward{Type: r.Type, ID: r.ID, Count: r.Count})
		}
		packs[id] = p
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,packId FROM MapTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, packID int
		if err = rows.Scan(&id, &packID); err != nil {
			return nil, err
		}
		if p, ok := packs[packID]; ok {
			p.MapIDs = append(p.MapIDs, id)
			packs[packID] = p
		}
	}
	return packs, rows.Err()
}
