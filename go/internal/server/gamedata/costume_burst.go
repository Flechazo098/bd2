package gamedata

import (
	"database/sql"
	"errors"
	"fmt"
)

// CostumeBurstLevel is one row of CostumeBurstTable.  Level is the database
// row id (and is the level unlocked by upgrading from Level-1).
type CostumeBurstLevel struct {
	CostumeID uint64
	Level     uint64
	Costs     []PromotionCost
}

// CostumeBurstUpgradeRule is the exact cost for the next burst level.
type CostumeBurstUpgradeRule struct {
	CostumeID    uint64
	CurrentLevel uint64
	NextLevel    uint64
	MaxLevel     uint64
	Costs        []PromotionCost
}

// CostumeBurstDesign contains only costumes which have a complete, contiguous
// CostumeBurstTable group.  GroupId is the CostumeTable id for burst-enabled
// costumes (the table also carries CostumeType for newer costume families).
type CostumeBurstDesign struct {
	Levels map[uint64]map[uint64]CostumeBurstLevel
}

func LoadCostumeBurstDesign(root, version string) (*CostumeBurstDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadCostumeBurstDesign(db)
}

func loadCostumeBurstDesign(db *sql.DB) (*CostumeBurstDesign, error) {
	if db == nil {
		return nil, errors.New("gamedata: nil costume burst database")
	}
	design := &CostumeBurstDesign{Levels: make(map[uint64]map[uint64]CostumeBurstLevel)}
	rows, err := db.Query("SELECT groupId,id,ProtoBuf FROM CostumeBurstTable ORDER BY groupId,id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var groupID, level uint64
		var raw []byte
		if err := rows.Scan(&groupID, &level, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if groupID == 0 || level == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid costume burst identity %d/%d", groupID, level)
		}
		groups, groupErr := packedInts(raw, 8)
		ids, idErr := packedInts(raw, 9)
		counts, countErr := packedInts(raw, 10)
		itemIDs, itemIDErr := packedInts(raw, 11)
		types, typeErr := packedInts(raw, 12)
		if groupErr != nil || idErr != nil || countErr != nil || itemIDErr != nil || typeErr != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: costume burst %d/%d has invalid fields", groupID, level)
		}
		if len(groups) != 1 || groups[0] != groupID || len(ids) != 1 || ids[0] != level {
			rows.Close()
			return nil, fmt.Errorf("gamedata: costume burst %d/%d has mismatched proto identity", groupID, level)
		}
		if len(counts) == 0 || len(counts) != len(itemIDs) || len(itemIDs) != len(types) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: costume burst %d/%d has mismatched costs", groupID, level)
		}
		entry := CostumeBurstLevel{CostumeID: groupID, Level: level}
		for i := range counts {
			if counts[i] == 0 || (types[i] == 4 && itemIDs[i] != 0) ||
				(types[i] == 8 && itemIDs[i] == 0) || (types[i] != 4 && types[i] != 8) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: costume burst %d/%d has invalid cost", groupID, level)
			}
			entry.Costs = append(entry.Costs, PromotionCost{Type: types[i], ID: itemIDs[i], Count: counts[i]})
		}
		if _, exists := design.Levels[groupID][level]; exists {
			rows.Close()
			return nil, fmt.Errorf("gamedata: duplicate costume burst %d/%d", groupID, level)
		}
		if design.Levels[groupID] == nil {
			design.Levels[groupID] = make(map[uint64]CostumeBurstLevel)
		}
		design.Levels[groupID][level] = entry
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(design.Levels) == 0 {
		return nil, errors.New("gamedata: CostumeBurstTable is empty")
	}
	for costumeID, levels := range design.Levels {
		for level := uint64(1); level <= uint64(len(levels)); level++ {
			if _, ok := levels[level]; !ok {
				return nil, fmt.Errorf("gamedata: costume burst %d missing level %d", costumeID, level)
			}
		}
	}
	return design, nil
}

// UpgradeRule returns the costs for currentLevel -> currentLevel+1.
func (d *CostumeBurstDesign) UpgradeRule(costumeID, currentLevel uint64) (CostumeBurstUpgradeRule, error) {
	if d == nil || costumeID == 0 {
		return CostumeBurstUpgradeRule{}, errors.New("gamedata: invalid costume burst lookup")
	}
	levels, ok := d.Levels[costumeID]
	if !ok {
		return CostumeBurstUpgradeRule{}, fmt.Errorf("gamedata: costume %d has no burst design", costumeID)
	}
	maxLevel := uint64(len(levels))
	if currentLevel >= maxLevel {
		return CostumeBurstUpgradeRule{}, fmt.Errorf("gamedata: costume %d burst is already at maximum level %d", costumeID, maxLevel)
	}
	next := currentLevel + 1
	entry, ok := levels[next]
	if !ok {
		return CostumeBurstUpgradeRule{}, fmt.Errorf("gamedata: costume %d missing burst level %d", costumeID, next)
	}
	return CostumeBurstUpgradeRule{
		CostumeID: costumeID, CurrentLevel: currentLevel, NextLevel: next,
		MaxLevel: maxLevel, Costs: append([]PromotionCost(nil), entry.Costs...),
	}, nil
}
