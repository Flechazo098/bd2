package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"database/sql"
	"fmt"
)

type FieldResearchObject struct {
	ID, Type, CollectionID, HoldingCollectionID, TalentObjectID int
	Maps                                                        []int
	QuestRange                                                  []uint64
	InteractionQuests                                           []int
	Reward                                                      Reward
}
type FieldResearchDesign struct{ Objects map[int]FieldResearchObject }

func openLogicalReadDB(root, version, name string) (*sql.DB, func(), error) {
	return OpenDatabase(root, version, name)
}
func LoadFieldResearch(root, version string, pack int) (FieldResearchDesign, error) {
	d := FieldResearchDesign{Objects: map[int]FieldResearchObject{}}
	db, done, err := openLogicalReadDB(root, version, fmt.Sprintf("pack%d", pack))
	if err != nil {
		return d, err
	}
	defer done()
	rows, err := db.Query("SELECT ProtoBuf FROM FieldResearchObjectTable")
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return d, err
		}
		var o FieldResearchObject
		values := []*int{&o.CollectionID, &o.HoldingCollectionID, &o.ID, &o.TalentObjectID, &o.Type}
		for i, f := range []int{1, 3, 5, 17, 18} {
			v, e := optionalScalar(raw, f)
			if e != nil {
				_ = rows.Close()
				return d, e
			}
			*values[i] = int(v)
		}
		o.QuestRange, err = packedInts(raw, 10)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		o.Reward.Type, err = optionalScalar(raw, 16)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		o.Reward.ID, err = optionalScalar(raw, 15)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		o.Reward.Count, err = optionalScalar(raw, 14)
		if err != nil {
			_ = rows.Close()
			return d, err
		}
		if o.ID <= 0 {
			_ = rows.Close()
			return d, fmt.Errorf("gamedata: invalid research id")
		}
		d.Objects[o.ID] = o
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return d, err
	}
	common, closeCommon, err := openStatDatabase(root, version)
	if err != nil {
		return d, err
	}
	defer closeCommon()
	maps := map[string]int{}
	rows, err = common.Query("SELECT id,mapScenePath FROM MapTable WHERE packId=?", pack)
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
		maps[scene] = id
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return d, err
	}
	scenes, closeScenes, err := openLogicalReadDB(root, version, "FieldObjectSceneData")
	if err != nil {
		return d, err
	}
	defer closeScenes()
	rows, err = scenes.Query(fmt.Sprintf("SELECT ProtoBuf FROM FieldObjectSceneData%d WHERE objectType=5", pack))
	if err != nil {
		return d, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return d, err
		}
		id, e := optionalScalar(raw, 8)
		if e != nil {
			return d, e
		}
		var scene string
		if e = wire.Walk(raw, func(f wire.Field) error {
			if f.Number == 7 && f.Type == 2 {
				scene = string(f.Value)
			}
			return nil
		}); e != nil {
			return d, e
		}
		o, ok := d.Objects[int(id)]
		if !ok {
			return d, fmt.Errorf("gamedata: scene references missing research %d", id)
		}
		mapID, ok := maps[scene]
		if !ok {
			return d, fmt.Errorf("gamedata: research scene %s missing map", scene)
		}
		o.Maps = append(o.Maps, mapID)
		d.Objects[o.ID] = o
	}
	if err = rows.Err(); err != nil {
		return d, err
	}
	_ = rows.Close()
	rows, err = common.Query(fmt.Sprintf("SELECT id,ProtoBuf FROM QuestTable%d", pack))
	if err != nil {
		return d, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return d, err
		}
		condition, e := optionalScalar(raw, 11)
		if e != nil {
			return d, e
		}
		if condition != 18 {
			continue
		}
		objects, e := packedInts(raw, 33)
		if e != nil {
			return d, e
		}
		for _, object := range objects {
			if o, ok := d.Objects[int(object)]; ok {
				o.InteractionQuests = append(o.InteractionQuests, id)
				d.Objects[o.ID] = o
			}
		}
	}
	return d, rows.Err()
}

// Research characters are identified by the client's FieldRewardResearch class
// (6), joined through CharTable's talent ID rather than a fixed character list.
func LoadResearchCharacters(root, version string) (map[uint64]bool, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	talents := map[uint64]bool{}
	rows, err := db.Query("SELECT id FROM TalentTable WHERE classType=6")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		talents[id] = true
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	chars := map[uint64]bool{}
	rows, err = db.Query("SELECT id,ProtoBuf FROM CharTable")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uint64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		talent, e := optionalScalar(raw, 18)
		if e != nil {
			return nil, e
		}
		if talents[talent] {
			chars[id] = true
		}
	}
	return chars, rows.Err()
}
