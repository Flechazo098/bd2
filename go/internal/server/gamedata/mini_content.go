package gamedata

import "fmt"

type MiniContentRoute struct {
	UID, ContentType, ContentID uint64
	Start, End                  int64
}

type MiniContentDesign struct {
	Stories map[uint64]Reward
	Groups  map[uint64][]uint64
}

func LoadMiniContentDesign(root, version string) (*MiniContentDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &MiniContentDesign{Stories: map[uint64]Reward{}, Groups: map[uint64][]uint64{}}
	rows, err := db.Query("SELECT ProtoBuf FROM DailyStoryTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		id, e := optionalScalar(raw, 2)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		var r Reward
		for field, target := range map[int]*uint64{10: &r.Type, 9: &r.ID, 8: &r.Count} {
			v, e := optionalScalar(raw, field)
			if e != nil {
				_ = rows.Close()
				return nil, e
			}
			*target = v
		}
		d.Stories[id] = r
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT ProtoBuf FROM MiniEventStoryTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		id, e := optionalScalar(raw, 1)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		group, e := optionalScalar(raw, 2)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		d.Groups[group] = append(d.Groups[group], id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err == nil {
		for group, ids := range d.Groups {
			for _, id := range ids {
				if _, exists := d.Stories[id]; !exists {
					return nil, fmt.Errorf("gamedata: mini story group %d references missing daily story %d", group, id)
				}
			}
		}
	}
	return d, err
}
