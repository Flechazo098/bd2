package gamedata

import (
	"database/sql"
	"fmt"
	"sort"
)

const (
	PictorialCharacter = 1
	PictorialTalent    = 2
	PictorialEquipment = 3
	PictorialCooking   = 4
	PictorialItem      = 5
	PictorialCostume   = 7
)

type PictorialBuff struct {
	ID       uint64
	Category uint64
	StatType uint64
	Value    float64
}

type PictorialBuffStat struct {
	Category uint64
	StatType uint64
	Value    float64
}

type CharacterPictorial struct {
	ID        uint64
	UniqueIDs []uint64
	BuffID    uint64
}

type TalentPictorial struct {
	ID        uint64
	TalentIDs []uint64
}

type CostumePictorial struct {
	ID         uint64
	CostumeID  uint64
	BuffIDs    []uint64
	Thresholds []uint64
}

type EquipmentPictorial struct {
	ID          uint64
	EquipmentID uint64
	BuffID      uint64
}

type CookingPictorial struct {
	ID       uint64
	RecipeID uint64
	BuffID   uint64
}

type ItemPictorial struct {
	ID       uint64
	ItemID   uint64
	BuffID   uint64
	Category uint64
}

type CharacterPictorialMeta struct {
	UniqueID         uint64
	TalentID         uint64
	UsePackTemporary bool
}

// PictorialDesign is immutable 2.34.13 design data. Dynamic account state is
// deliberately not copied here: callers match their current ownership against
// these rows each time they build PictorialBookInfo or AllCharRefresh.
type PictorialDesign struct {
	Root       string
	Version    string
	Characters []CharacterPictorial
	Talents    []TalentPictorial
	Costumes   []CostumePictorial
	Equipment  []EquipmentPictorial
	Cooking    []CookingPictorial
	Items      []ItemPictorial
	Buffs      map[uint64]PictorialBuff
	CharMeta   map[uint64]CharacterPictorialMeta
}

func LoadPictorialDesign(root, version string) (*PictorialDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	d := &PictorialDesign{Root: root, Version: version, Buffs: map[uint64]PictorialBuff{}, CharMeta: map[uint64]CharacterPictorialMeta{}}
	if err := loadProtoRows(db, "CharacterPictorialBookTable", func(proto []byte) error {
		id, err := exactlyOne(proto, 5)
		if err != nil {
			return err
		}
		buff, err := exactlyOne(proto, 2)
		if err != nil {
			return err
		}
		ids, err := packedInts(proto, 1)
		if err != nil || len(ids) == 0 {
			return fmt.Errorf("gamedata: character pictorial %d ids: %w", id, err)
		}
		d.Characters = append(d.Characters, CharacterPictorial{ID: id, UniqueIDs: ids, BuffID: buff})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadProtoRows(db, "TalentPictorialBookTable", func(proto []byte) error {
		id, err := exactlyOne(proto, 1)
		if err != nil {
			return err
		}
		ids, err := packedInts(proto, 2)
		if err != nil || len(ids) == 0 {
			return fmt.Errorf("gamedata: talent pictorial %d ids: %w", id, err)
		}
		d.Talents = append(d.Talents, TalentPictorial{ID: id, TalentIDs: ids})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadProtoRows(db, "CostumePictorialBookTable", func(proto []byte) error {
		id, err := exactlyOne(proto, 5)
		if err != nil {
			return err
		}
		costumeID, err := exactlyOne(proto, 3)
		if err != nil {
			return err
		}
		buffs, err := packedInts(proto, 2)
		if err != nil {
			return err
		}
		thresholds, err := packedInts(proto, 4)
		if err != nil || len(buffs) == 0 || len(buffs) != len(thresholds) {
			return fmt.Errorf("gamedata: costume pictorial %d has mismatched buff thresholds", id)
		}
		d.Costumes = append(d.Costumes, CostumePictorial{ID: id, CostumeID: costumeID, BuffIDs: buffs, Thresholds: thresholds})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadProtoRows(db, "EquipmentPictorialBookTable", func(proto []byte) error {
		id, err := exactlyOne(proto, 5)
		if err != nil {
			return err
		}
		equipmentID, err := exactlyOne(proto, 4)
		if err != nil {
			return err
		}
		buff, err := exactlyOne(proto, 2)
		if err != nil {
			return err
		}
		d.Equipment = append(d.Equipment, EquipmentPictorial{ID: id, EquipmentID: equipmentID, BuffID: buff})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadProtoRows(db, "CookingPictorialBookTable", func(proto []byte) error {
		buff, err := exactlyOne(proto, 1)
		if err != nil {
			return err
		}
		recipe, err := exactlyOne(proto, 2)
		if err != nil {
			return err
		}
		id, err := exactlyOne(proto, 3)
		if err != nil {
			return err
		}
		d.Cooking = append(d.Cooking, CookingPictorial{ID: id, RecipeID: recipe, BuffID: buff})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadProtoRows(db, "ItemPictorialBookTable", func(proto []byte) error {
		category, err := zeroOrOne(proto, 1)
		if err != nil {
			return err
		}
		buff, err := exactlyOne(proto, 2)
		if err != nil {
			return err
		}
		id, err := exactlyOne(proto, 3)
		if err != nil {
			return err
		}
		item, err := exactlyOne(proto, 4)
		if err != nil {
			return err
		}
		d.Items = append(d.Items, ItemPictorial{ID: id, ItemID: item, BuffID: buff, Category: category})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadProtoRows(db, "AdventuregroupBuffTable", func(proto []byte) error {
		id, err := exactlyOne(proto, 2)
		if err != nil {
			return err
		}
		stat, err := exactlyOne(proto, 3)
		if err != nil {
			return err
		}
		category, err := zeroOrOne(proto, 1)
		if err != nil {
			return err
		}
		value, found, err := fixed64Double(proto, 4)
		if err != nil {
			return fmt.Errorf("gamedata: pictorial buff %d value: %w", id, err)
		}
		if !found || value <= 0 {
			// Many collection-discovery rows have no battle-stat reward.
			return nil
		}
		d.Buffs[id] = PictorialBuff{ID: id, Category: category, StatType: stat, Value: value}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := loadProtoRows(db, "CharTable", func(proto []byte) error {
		id, err := exactlyOne(proto, 12)
		if err != nil {
			return err
		}
		uniqueID, err := exactlyOne(proto, 20)
		if err != nil {
			return err
		}
		talentID, err := zeroOrOne(proto, 18)
		if err != nil {
			return err
		}
		temporary, err := zeroOrOne(proto, 21)
		if err != nil {
			return err
		}
		d.CharMeta[id] = CharacterPictorialMeta{UniqueID: uniqueID, TalentID: talentID, UsePackTemporary: temporary != 0}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(d.Characters, func(i, j int) bool { return d.Characters[i].ID < d.Characters[j].ID })
	sort.Slice(d.Talents, func(i, j int) bool { return d.Talents[i].ID < d.Talents[j].ID })
	sort.Slice(d.Costumes, func(i, j int) bool { return d.Costumes[i].ID < d.Costumes[j].ID })
	sort.Slice(d.Equipment, func(i, j int) bool { return d.Equipment[i].ID < d.Equipment[j].ID })
	sort.Slice(d.Cooking, func(i, j int) bool { return d.Cooking[i].ID < d.Cooking[j].ID })
	sort.Slice(d.Items, func(i, j int) bool { return d.Items[i].ID < d.Items[j].ID })
	return d, nil
}

func loadProtoRows(db *sql.DB, table string, consume func([]byte) error) error {
	rows, err := db.Query("SELECT ProtoBuf FROM " + table)
	if err != nil {
		return fmt.Errorf("gamedata: query %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var proto []byte
		if err := rows.Scan(&proto); err != nil {
			return err
		}
		if err := consume(proto); err != nil {
			return fmt.Errorf("gamedata: decode %s: %w", table, err)
		}
	}
	return rows.Err()
}

func exactlyOne(proto []byte, field int) (uint64, error) {
	values, err := packedInts(proto, field)
	if err != nil || len(values) != 1 {
		return 0, fmt.Errorf("field %d values %v: %w", field, values, err)
	}
	return values[0], nil
}

func zeroOrOne(proto []byte, field int) (uint64, error) {
	values, err := packedInts(proto, field)
	if err != nil || len(values) > 1 {
		return 0, fmt.Errorf("field %d values %v: %w", field, values, err)
	}
	if len(values) == 0 {
		return 0, nil
	}
	return values[0], nil
}
