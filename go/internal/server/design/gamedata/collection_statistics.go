package gamedata

import "fmt"

type StatisticsCharacter struct{ UniqueID, Grade uint64 }
type StatisticsEquipment struct{ UniqueID, Slot, Grade, CharacterUniqueID uint64 }
type CollectionStatisticsDesign struct {
	Characters      map[uint64]StatisticsCharacter
	CostumeGrade    map[uint64]uint64
	Equipment       map[uint64]StatisticsEquipment
	CastleStages    map[uint64]map[uint64]uint64
	PVPRankGroups   map[uint64]uint64
	PVPMinimumGroup uint64
	GachaGroups     map[uint64]uint64
}

func LoadCollectionStatistics(root, version string) (*CollectionStatisticsDesign, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	d := &CollectionStatisticsDesign{Characters: map[uint64]StatisticsCharacter{}, CostumeGrade: map[uint64]uint64{}, Equipment: map[uint64]StatisticsEquipment{}, CastleStages: map[uint64]map[uint64]uint64{}}
	uniqueGrade := map[uint64]uint64{}
	read := func(table string, visit func(uint64, []byte) error) error {
		rows, e := db.Query("SELECT id,ProtoBuf FROM " + table)
		if e != nil {
			return e
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id uint64
			var b []byte
			if e = rows.Scan(&id, &b); e != nil {
				return e
			}
			if e = visit(id, b); e != nil {
				return fmt.Errorf("gamedata: %s %d: %w", table, id, e)
			}
		}
		return rows.Err()
	}
	if err = read("CharTable", func(id uint64, b []byte) error {
		var c StatisticsCharacter
		if e := pvpScalars(b, map[int]*uint64{9: &c.Grade, 20: &c.UniqueID}); e != nil {
			return e
		}
		d.Characters[id] = c
		uniqueGrade[c.UniqueID] = c.Grade
		return nil
	}); err != nil {
		return nil, err
	}
	if err = read("CostumeTable", func(id uint64, b []byte) error {
		var unique uint64
		if e := pvpScalars(b, map[int]*uint64{27: &unique}); e != nil {
			return e
		}
		d.CostumeGrade[id] = uniqueGrade[unique]
		return nil
	}); err != nil {
		return nil, err
	}
	if err = read("EquipmentTable", func(id uint64, b []byte) error {
		var v StatisticsEquipment
		if e := pvpScalars(b, map[int]*uint64{3: &v.Grade, 16: &v.CharacterUniqueID, 20: &v.Slot, 22: &v.UniqueID}); e != nil {
			return e
		}
		d.Equipment[id] = v
		return nil
	}); err != nil {
		return nil, err
	}
	castle, err := LoadCastleRewardDesign(root, version)
	if err != nil {
		return nil, err
	}
	d.CastleStages[7] = map[uint64]uint64{}
	for id, f := range castle.Floors {
		for _, battle := range f.Monsters {
			d.CastleStages[7][battle] = id
		}
	}
	for _, tower := range castle.Towers {
		if d.CastleStages[tower.Mode] == nil {
			d.CastleStages[tower.Mode] = map[uint64]uint64{}
		}
		d.CastleStages[tower.Mode][tower.Deck] = tower.ID
	}
	d.PVPRankGroups = map[uint64]uint64{}
	if err = read("PvpRankTable", func(id uint64, b []byte) error {
		var group uint64
		if e := pvpScalars(b, map[int]*uint64{14: &group}); e != nil {
			return e
		}
		d.PVPRankGroups[id] = group
		return nil
	}); err != nil {
		return nil, err
	}
	if err = read("PvpDefaultTable", func(id uint64, b []byte) error { return pvpScalars(b, map[int]*uint64{22: &d.PVPMinimumGroup}) }); err != nil {
		return nil, err
	}
	d.GachaGroups = map[uint64]uint64{}
	if err = read("GachaGroupTable", func(group uint64, b []byte) error {
		for _, field := range []int{24, 33} {
			ids, e := packedInts(b, field)
			if e != nil {
				return e
			}
			for _, id := range ids {
				if id == 0 {
					continue
				}
				if old := d.GachaGroups[id]; old != 0 && old != group {
					return fmt.Errorf("gacha %d has ambiguous history groups", id)
				}
				d.GachaGroups[id] = group
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return d, nil
}
