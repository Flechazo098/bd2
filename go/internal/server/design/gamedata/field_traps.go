package gamedata

import (
	"fmt"
	"slices"
)

type FieldTrap struct {
	ID, MapID, SwitchID, ResetType, Type int
	FieldBuff, CoolSeconds, RegenSeconds uint64
	DefaultEnabled                       bool
	QuestRange                           []uint64
	Maps                                 []int
}
type FieldTrapTrigger struct {
	ID, Type, AutoInteraction int
	QuestRange                []uint64
	Maps                      []int
}
type FieldTrapSwitch struct {
	ID, ObjectType, OrderType int
	Objects                   []uint64
}
type FieldTrapDesign struct {
	Traps           map[int]FieldTrap
	Triggers        map[int]FieldTrapTrigger
	Switches        map[int]FieldTrapSwitch
	MapIDs          []int
	StoryModeImmune bool
}

func LoadFieldTraps(root, version string, pack int) (FieldTrapDesign, error) {
	d := FieldTrapDesign{Traps: map[int]FieldTrap{}, Triggers: map[int]FieldTrapTrigger{}, Switches: map[int]FieldTrapSwitch{}}
	db, done, err := OpenDatabase(root, version, fmt.Sprintf("pack%d", pack))
	if err != nil {
		return d, err
	}
	defer done()
	for _, table := range []string{"FieldTrapTable", "TriggerTable", "FieldObjectSwitchTable"} {
		rows, e := db.Query("SELECT id,ProtoBuf FROM " + table + " ORDER BY id")
		if e != nil {
			return d, e
		}
		for rows.Next() {
			var id int
			var raw []byte
			if e = rows.Scan(&id, &raw); e != nil {
				_ = rows.Close()
				return d, e
			}
			values := map[int]uint64{}
			for _, field := range []int{2, 3, 4, 5, 8, 9, 11, 13, 14, 17, 18} {
				if table == "TriggerTable" && (field == 2 || field == 4 || field == 13) || table == "FieldObjectSwitchTable" && field == 4 || table == "FieldTrapTable" && field == 11 {
					continue
				}
				values[field], e = optionalScalar(raw, field)
				if e != nil {
					_ = rows.Close()
					return d, e
				}
			}
			switch table {
			case "FieldTrapTable":
				q, e := packedInts(raw, 11)
				if e != nil {
					_ = rows.Close()
					return d, e
				}
				if values[3] > 1 || values[14] > 1 || values[4] > 0x7fffffff || values[13] > 0x7fffffff {
					_ = rows.Close()
					return d, fmt.Errorf("gamedata: invalid trap %d pack %d", id, pack)
				}
				d.Traps[id] = FieldTrap{ID: id, MapID: int(values[8]), SwitchID: int(values[17]), ResetType: int(values[14]), Type: int(values[18]), FieldBuff: values[5], CoolSeconds: values[4], RegenSeconds: values[13], DefaultEnabled: values[3] == 1, QuestRange: q}
			case "TriggerTable":
				q, e := packedInts(raw, 12)
				if e != nil {
					_ = rows.Close()
					return d, e
				}
				d.Triggers[id] = FieldTrapTrigger{ID: id, Type: int(values[11]), AutoInteraction: int(values[9]), QuestRange: q}
			case "FieldObjectSwitchTable":
				objects, e := packedInts(raw, 4)
				if e != nil {
					_ = rows.Close()
					return d, e
				}
				d.Switches[id] = FieldTrapSwitch{ID: id, ObjectType: int(values[2]), OrderType: int(values[5]), Objects: objects}
			}
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return d, e
		}
	}
	common, closeCommon, err := openStatDatabase(root, version)
	if err != nil {
		return d, err
	}
	defer closeCommon()
	var packRaw []byte
	if err := common.QueryRow("SELECT ProtoBuf FROM PackTable WHERE id=?", pack).Scan(&packRaw); err != nil {
		return d, err
	}
	packType, err := optionalScalar(packRaw, 55)
	if err != nil {
		return d, err
	}
	// FieldDeckPacket.IsUsingStoryMode includes PackInfo.IsNewFieldSpecPack.
	d.StoryModeImmune = packType == 0 || packType == 1000 || packType == 1 && pack >= 1010 || packType == 6 && pack >= 2009
	mapScenes := map[string]int{}
	rows, err := common.Query("SELECT id,mapScenePath FROM MapTable WHERE packId=? ORDER BY id", pack)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var id int
		var scene string
		if err = rows.Scan(&id, &scene); err != nil {
			_ = rows.Close()
			return d, err
		}
		mapScenes[scene] = id
		d.MapIDs = append(d.MapIDs, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return d, err
	}
	scenes, closeScenes, err := OpenDatabase(root, version, "FieldObjectSceneData")
	if err != nil {
		return d, err
	}
	defer closeScenes()
	rows, err = scenes.Query(fmt.Sprintf("SELECT tableId,sceneName,objectType FROM FieldObjectSceneData%d WHERE objectType IN (7,15,16)", pack))
	if err != nil {
		return d, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, kind int
		var scene string
		if err = rows.Scan(&id, &scene, &kind); err != nil {
			return d, err
		}
		if id == 0 {
			continue
		}
		mapID, found := mapScenes[scene]
		if !found {
			continue
		}
		if kind == 15 {
			trigger, found := d.Triggers[id]
			if !found {
				return d, fmt.Errorf("gamedata: missing trigger %d pack %d", id, pack)
			}
			if !slices.Contains(trigger.Maps, mapID) {
				trigger.Maps = append(trigger.Maps, mapID)
			}
			d.Triggers[id] = trigger
		} else {
			trap, found := d.Traps[id]
			if !found {
				return d, fmt.Errorf("gamedata: missing trap %d pack %d", id, pack)
			}
			if !slices.Contains(trap.Maps, mapID) {
				trap.Maps = append(trap.Maps, mapID)
			}
			d.Traps[id] = trap
		}
	}
	return d, rows.Err()
}
