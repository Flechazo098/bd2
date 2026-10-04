package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

// EquipmentStatRule retains the exact option curve. Percent values are already
// fractions in GameData: 0.0135 means 1.35%, with no conversion by 100.
type EquipmentStatRule struct {
	Default, Growth float64
	Levels          []float64
	Ranks           [3][]float64
}

// EquipmentStatDesign contains the HP subset used for field maximum health.
// Loading once avoids decrypting common.db on every character lookup.
type EquipmentStatDesign struct {
	Options map[[2]uint64]EquipmentStatRule
}

func LoadEquipmentStatDesign(root, version string) (*EquipmentStatDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadEquipmentStatDesign(db)
}

func loadEquipmentStatDesign(db *sql.DB) (*EquipmentStatDesign, error) {
	rows, err := db.Query("SELECT groupId,id,ProtoBuf FROM EquipmentOptionTable WHERE id IN (1,2)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d := &EquipmentStatDesign{Options: map[[2]uint64]EquipmentStatRule{}}
	for rows.Next() {
		var group, id uint64
		var raw []byte
		if err := rows.Scan(&group, &id, &raw); err != nil {
			return nil, err
		}
		protoGroup, err := packedInts(raw, 3)
		if err != nil || len(protoGroup) != 1 || protoGroup[0] != group {
			return nil, fmt.Errorf("gamedata: invalid equipment stat group %d", group)
		}
		protoID, err := packedInts(raw, 5)
		if err != nil || len(protoID) != 1 || protoID[0] != id {
			return nil, fmt.Errorf("gamedata: invalid equipment stat id %d/%d", group, id)
		}
		var rule EquipmentStatRule
		rule.Default, _, err = fixed64Double(raw, 1)
		if err != nil {
			return nil, err
		}
		rule.Growth, _, err = fixed64Double(raw, 4)
		if err != nil {
			return nil, err
		}
		rule.Levels, err = fixed32Floats(raw, 6)
		if err != nil {
			return nil, err
		}
		for i := range rule.Ranks {
			rule.Ranks[i], err = fixed32Floats(raw, 7+i)
			if err != nil {
				return nil, err
			}
		}
		values := []float64{rule.Default, rule.Growth}
		values = append(values, rule.Levels...)
		for _, ranks := range rule.Ranks {
			values = append(values, ranks...)
		}
		for _, value := range values {
			if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("gamedata: invalid equipment stat %d/%d", group, id)
			}
		}
		d.Options[[2]uint64{group, id}] = rule
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(d.Options) == 0 {
		return nil, fmt.Errorf("gamedata: empty equipment health stat design")
	}
	return d, nil
}

// HealthContribution follows EquipmentInfo: main/private options use the
// level-and-rank curve, while suboptions use DefaultValue alone.
func (d *EquipmentStatDesign) HealthContribution(option EquipmentOption, suboption bool) (StatContribution, error) {
	if option.ID == 0 || option.ID > 2 {
		return StatContribution{}, nil
	}
	if d == nil || option.GroupID == 0 || option.Level < 0 {
		return StatContribution{}, fmt.Errorf("gamedata: invalid equipment health stat option")
	}
	rule, found := d.Options[[2]uint64{option.GroupID, option.ID}]
	if !found {
		return StatContribution{}, fmt.Errorf("gamedata: unknown equipment health option %d/%d", option.GroupID, option.ID)
	}
	return equipmentStatContribution(rule, option, suboption)
}

func equipmentStatContribution(rule EquipmentStatRule, option EquipmentOption, suboption bool) (StatContribution, error) {
	value := rule.Default
	if !suboption {
		var level float64
		if len(rule.Levels) != 0 {
			if option.Level >= len(rule.Levels) {
				return StatContribution{}, fmt.Errorf("gamedata: invalid equipment health option level %d", option.Level)
			}
			level = rule.Levels[option.Level]
		}
		var ranks float32
		for i, rank := range option.Rank {
			// EquipmentInfo.GetEquipOptionByRank returns zero for an empty
			// RankValueN curve, even when the equipment has a rank there.
			if rank == 0 || len(rule.Ranks[i]) == 0 {
				continue
			}
			if rank < 1 || rank > len(rule.Ranks[i]) {
				return StatContribution{}, fmt.Errorf("gamedata: invalid equipment health option rank %d", rank)
			}
			ranks += float32(rule.Ranks[i][rank-1])
		}
		value += rule.Growth * (level + float64(ranks))
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return StatContribution{}, fmt.Errorf("gamedata: invalid equipment health stat result")
	}
	return optionContribution(option.ID, value)
}
