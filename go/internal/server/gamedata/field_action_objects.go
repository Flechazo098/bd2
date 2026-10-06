package gamedata

import (
	"database/sql"
	"fmt"
)

type FieldActionObject struct {
	ID, GroupID, MapID, QuestID, QuestEnableType, Type, RegenSeconds int
}

func loadFieldActionObjects(db *sql.DB) (map[int]FieldActionObject, error) {
	out := map[int]FieldActionObject{}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('FieldActionObjectTable','FieldActionObjectGroupTable')").Scan(&tables); err != nil {
		return nil, err
	}
	if tables == 0 {
		return out, nil
	}
	if tables != 2 {
		return nil, fmt.Errorf("gamedata: incomplete field action tables")
	}
	groups := map[int]FieldActionObject{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM FieldActionObjectGroupTable")
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
		group := FieldActionObject{GroupID: id}
		for field, dst := range map[int]*int{3: &group.RegenSeconds, 4: &group.Type} {
			v, e := optionalScalar(raw, field)
			if e != nil || v > 0x7fffffff {
				rows.Close()
				return nil, fmt.Errorf("gamedata: invalid action group %d", id)
			}
			*dst = int(v)
		}
		if id <= 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid action group identity")
		}
		groups[id] = group
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM FieldActionObjectTable")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		groupID, e := optionalScalar(raw, 4)
		group, found := groups[int(groupID)]
		if e != nil || groupID > 0x7fffffff || !found || id <= 0 {
			return nil, fmt.Errorf("gamedata: invalid action object group %d", id)
		}
		group.ID = id
		for field, dst := range map[int]*int{8: &group.MapID, 10: &group.QuestEnableType, 11: &group.QuestID} {
			v, e := optionalScalar(raw, field)
			if e != nil || v > 0x7fffffff {
				return nil, fmt.Errorf("gamedata: invalid action object %d", id)
			}
			*dst = int(v)
		}
		if group.MapID <= 0 || group.QuestEnableType > 2 {
			return nil, fmt.Errorf("gamedata: invalid action object map/quest %d", id)
		}
		out[id] = group
	}
	return out, rows.Err()
}
