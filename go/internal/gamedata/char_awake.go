package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// CharAwakeCost is one exact CharAwakeGrowthTable material row. Type 4 is
// gold (and therefore has ID 0); type 8 is a mutable ResourceTable item.
type CharAwakeCost struct{ Type, ID, Count uint64 }

// CharAwakeGrowth is one cumulative imprint level or one awakening effect.
// Imprint StatValue is the value at that target level, not a per-level delta.
type CharAwakeGrowth struct {
	ID        uint64
	Costs     []CharAwakeCost
	StatType  uint64
	StatValue float64
}

type CharAwakeCharacter struct {
	UniqueCharID  uint64
	Active        bool
	ImprintIDs    [3]uint64
	ImprintGrowth [3][]CharAwakeGrowth
	AwakeGrowth   []CharAwakeGrowth
}

type CharAwakeCharacterStage struct {
	UniqueCharID uint64
	Grade        uint64
	GrowthGrade  uint64
	MaximumLevel uint64
}

// CharAwakeDesign is immutable 2.34.13 design data. Account progress remains
// in CollectionStore and is indexed by UniqueCharId, as CharAwakeDBInfo is.
type CharAwakeDesign struct {
	Characters map[uint64]CharAwakeCharacter
	Stages     map[uint64]CharAwakeCharacterStage
}

type CharImprintTarget struct {
	Slot        uint64
	TargetLevel uint64
}

func LoadCharAwakeDesign(root, version string) (*CharAwakeDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-char-awake-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadCharAwakeDesign(db)
}

func loadCharAwakeDesign(db *sql.DB) (*CharAwakeDesign, error) {
	growth := make(map[uint64]CharAwakeGrowth)
	rows, err := db.Query("SELECT id,ProtoBuf FROM CharAwakeGrowthTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		counts, _ := packedInts(proto, 1)
		ids, _ := packedInts(proto, 2)
		types, _ := packedInts(proto, 3)
		protoID, _ := packedInts(proto, 4)
		statType, _ := packedInts(proto, 5)
		statValue, hasStatValue, err := fixed64Double(proto, 6)
		if err != nil || len(protoID) != 1 || protoID[0] != id || len(statType) != 1 || !hasStatValue || math.IsNaN(statValue) || math.IsInf(statValue, 0) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid CharAwakeGrowthTable row %d", id)
		}
		if len(counts) != len(ids) || len(counts) != len(types) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: mismatched CharAwakeGrowthTable costs %d", id)
		}
		entry := CharAwakeGrowth{ID: id, StatType: statType[0], StatValue: statValue}
		if entry.StatType == 0 || entry.StatType > 20 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: unsupported awakening stat type %d in row %d", entry.StatType, id)
		}
		for i := range counts {
			if counts[i] == 0 || (types[i] != 4 && types[i] != 8) || (types[i] == 4 && ids[i] != 0) || (types[i] == 8 && ids[i] == 0) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: invalid awakening cost in row %d", id)
			}
			entry.Costs = append(entry.Costs, CharAwakeCost{Type: types[i], ID: ids[i], Count: counts[i]})
		}
		growth[id] = entry
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	imprints := make(map[uint64][]CharAwakeGrowth)
	rows, err = db.Query("SELECT id,ProtoBuf FROM CharImprintTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		growthIDs, _ := packedInts(proto, 1)
		protoID, _ := packedInts(proto, 2)
		if len(protoID) != 1 || protoID[0] != id || len(growthIDs) == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid CharImprintTable row %d", id)
		}
		for _, growthID := range growthIDs {
			entry, ok := growth[growthID]
			if !ok || len(entry.Costs) == 0 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: imprint %d has invalid growth %d", id, growthID)
			}
			imprints[id] = append(imprints[id], entry)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	design := &CharAwakeDesign{Characters: map[uint64]CharAwakeCharacter{}, Stages: map[uint64]CharAwakeCharacterStage{}}
	rows, err = db.Query("SELECT id,ProtoBuf FROM CharAwakeTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rowID uint64
		var proto []byte
		if err := rows.Scan(&rowID, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		active, _ := packedInts(proto, 1)
		awakeIDs, _ := packedInts(proto, 2)
		unique, _ := packedInts(proto, 3)
		slot1, _ := packedInts(proto, 4)
		slot2, _ := packedInts(proto, 5)
		slot3, _ := packedInts(proto, 6)
		if len(active) != 1 || active[0] != 1 || len(unique) != 1 || unique[0] != rowID || len(slot1) != 1 || len(slot2) != 1 || len(slot3) != 1 || len(awakeIDs) == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid CharAwakeTable row %d", rowID)
		}
		entry := CharAwakeCharacter{UniqueCharID: rowID, Active: true, ImprintIDs: [3]uint64{slot1[0], slot2[0], slot3[0]}}
		for i, imprintID := range entry.ImprintIDs {
			levels, ok := imprints[imprintID]
			if !ok {
				rows.Close()
				return nil, fmt.Errorf("gamedata: awakening %d has unknown imprint %d", rowID, imprintID)
			}
			entry.ImprintGrowth[i] = append([]CharAwakeGrowth(nil), levels...)
		}
		for i, growthID := range awakeIDs {
			awakeGrowth, ok := growth[growthID]
			if !ok || (i > 0 && len(awakeGrowth.Costs) != 0) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: awakening %d has invalid growth %d", rowID, growthID)
			}
			entry.AwakeGrowth = append(entry.AwakeGrowth, awakeGrowth)
		}
		if len(entry.AwakeGrowth[0].Costs) == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: awakening %d has no activation cost", rowID)
		}
		design.Characters[rowID] = entry
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = db.Query("SELECT id,ProtoBuf FROM CharTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		growthID, _ := packedInts(proto, 1)
		grade, _ := packedInts(proto, 9)
		growthGrade, _ := packedInts(proto, 10)
		unique, _ := packedInts(proto, 20)
		if len(unique) != 1 || design.Characters[unique[0]].UniqueCharID == 0 {
			continue
		}
		if len(growthID) != 1 || len(grade) != 1 || len(growthGrade) != 1 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid awakening character stage %d", id)
		}
		var growthProto []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growthID[0]).Scan(&growthProto); err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: awakening character %d growth: %w", id, err)
		}
		maximum, _ := packedInts(growthProto, 9)
		if len(maximum) != 1 || maximum[0] == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid awakening character maximum %d", id)
		}
		design.Stages[id] = CharAwakeCharacterStage{UniqueCharID: unique[0], Grade: grade[0], GrowthGrade: growthGrade[0], MaximumLevel: maximum[0]}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(design.Characters) == 0 || len(design.Stages) == 0 {
		return nil, fmt.Errorf("gamedata: awakening design is empty")
	}
	return design, nil
}

func (d *CharAwakeDesign) CharacterUniqueID(characterID uint64) (uint64, bool) {
	if d == nil {
		return 0, false
	}
	stage, ok := d.Stages[characterID]
	return stage.UniqueCharID, ok
}

// ValidateGrowthCompleted mirrors CommonPacket.IsCharGrowthLevelCompleted:
// the current CharTable stage must have Growthgrade == Grade and its own
// CharGrowthTable.MaxLevel must have been reached.
func (d *CharAwakeDesign) ValidateGrowthCompleted(characterID, level uint64) (uint64, error) {
	if d == nil {
		return 0, fmt.Errorf("gamedata: awakening design unavailable")
	}
	stage, ok := d.Stages[characterID]
	if !ok || stage.UniqueCharID == 0 {
		return 0, fmt.Errorf("gamedata: character %d has no awakening design", characterID)
	}
	if stage.Grade != stage.GrowthGrade || level < stage.MaximumLevel {
		return 0, fmt.Errorf("gamedata: character %d has not completed growth", characterID)
	}
	return stage.UniqueCharID, nil
}

func appendAwakeCosts(dst []CharAwakeCost, src []CharAwakeCost) ([]CharAwakeCost, error) {
	for _, cost := range src {
		merged := false
		for i := range dst {
			if dst[i].Type != cost.Type || dst[i].ID != cost.ID {
				continue
			}
			if cost.Count > ^uint64(0)-dst[i].Count {
				return nil, fmt.Errorf("gamedata: awakening cost overflow")
			}
			dst[i].Count += cost.Count
			merged = true
			break
		}
		if !merged {
			dst = append(dst, cost)
		}
	}
	return dst, nil
}

func (d *CharAwakeDesign) ImprintCosts(uniqueCharID uint64, current [3]uint64, targets []CharImprintTarget) ([]CharAwakeCost, [3]uint64, error) {
	entry, ok := d.Characters[uniqueCharID]
	if !ok || !entry.Active || len(targets) == 0 {
		return nil, current, fmt.Errorf("gamedata: invalid imprint request for character %d", uniqueCharID)
	}
	next := current
	seen := [3]bool{}
	var costs []CharAwakeCost
	for _, target := range targets {
		if target.Slot < 1 || target.Slot > 3 || seen[target.Slot-1] {
			return nil, current, fmt.Errorf("gamedata: invalid or duplicate imprint slot %d", target.Slot)
		}
		seen[target.Slot-1] = true
		slot := target.Slot - 1
		maximum := uint64(len(entry.ImprintGrowth[slot]))
		if target.TargetLevel <= current[slot] || target.TargetLevel > maximum {
			return nil, current, fmt.Errorf("gamedata: invalid imprint target %d for slot %d at %d/%d", target.TargetLevel, target.Slot, current[slot], maximum)
		}
		for level := current[slot] + 1; level <= target.TargetLevel; level++ {
			var err error
			costs, err = appendAwakeCosts(costs, entry.ImprintGrowth[slot][level-1].Costs)
			if err != nil {
				return nil, current, err
			}
		}
		next[slot] = target.TargetLevel
	}
	return costs, next, nil
}

func (d *CharAwakeDesign) AwakeCosts(uniqueCharID uint64, levels [3]uint64, isAwake bool) ([]CharAwakeCost, error) {
	entry, ok := d.Characters[uniqueCharID]
	if !ok || !entry.Active || isAwake {
		return nil, fmt.Errorf("gamedata: character %d cannot awaken", uniqueCharID)
	}
	for i := range levels {
		if levels[i] != uint64(len(entry.ImprintGrowth[i])) {
			return nil, fmt.Errorf("gamedata: character %d imprint slot %d is not complete", uniqueCharID, i+1)
		}
	}
	return append([]CharAwakeCost(nil), entry.AwakeGrowth[0].Costs...), nil
}

func (d *CharAwakeDesign) ValidateProgress(uniqueCharID uint64, levels [3]uint64, isAwake bool) error {
	entry, ok := d.Characters[uniqueCharID]
	if !ok || !entry.Active {
		return fmt.Errorf("gamedata: character %d has no awakening design", uniqueCharID)
	}
	for i, level := range levels {
		maximum := uint64(len(entry.ImprintGrowth[i]))
		if level > maximum || (isAwake && level != maximum) {
			return fmt.Errorf("gamedata: invalid saved awakening slot %d level %d/%d", i+1, level, maximum)
		}
	}
	return nil
}

// CharAwakeContributions reproduces the client aggregation: each imprint slot
// contributes only its target-level cumulative row, while awakening adds all
// CharAwakeTable.GrowthId stat rows.
func (d *CharAwakeDesign) CharAwakeContributions(uniqueCharID uint64, levels [3]uint64, isAwake bool) ([]StatContribution, error) {
	if err := d.ValidateProgress(uniqueCharID, levels, isAwake); err != nil {
		return nil, err
	}
	entry := d.Characters[uniqueCharID]
	var growth []CharAwakeGrowth
	for i, level := range levels {
		if level > uint64(len(entry.ImprintGrowth[i])) {
			return nil, fmt.Errorf("gamedata: imprint slot %d level %d exceeds maximum", i+1, level)
		}
		if level != 0 {
			growth = append(growth, entry.ImprintGrowth[i][level-1])
		}
	}
	if isAwake {
		growth = append(growth, entry.AwakeGrowth...)
	}
	result := make([]StatContribution, 0, len(growth))
	for _, row := range growth {
		contribution := StatContribution{Option: row.StatType}
		switch row.StatType {
		case 1, 2:
			contribution.Stat = StatHealth
		case 3, 4:
			contribution.Stat = StatAttack
		case 5, 6:
			contribution.Stat = StatMagic
		case 7:
			contribution.Stat = StatDefencePercent
		case 8:
			contribution.Stat = StatMagicResistancePercent
		case 9:
			contribution.Stat = StatCriticalChance
		case 10:
			contribution.Stat = StatCriticalDamage
		case 11, 12, 13, 14, 15, 19:
			contribution.Stat = StatElementDamage
		case 16, 17, 18, 20:
			contribution.Stat = StatElementResistance
		default:
			return nil, fmt.Errorf("gamedata: unsupported awakening stat option %d", row.StatType)
		}
		if row.StatType == 2 || row.StatType == 4 || row.StatType == 6 {
			contribution.Percent = row.StatValue
		} else {
			contribution.Flat = row.StatValue
		}
		result = append(result, contribution)
	}
	return result, nil
}
