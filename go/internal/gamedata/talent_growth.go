package gamedata

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// CharacterTalent identifies the GameData rows which govern one character's
// talent. MaxLevel is deliberately read per talent: not every talent has five
// levels.
type CharacterTalent struct {
	TalentID    uint64
	GrowthGroup uint64
	MaxLevel    uint64
}

// TalentGrowthLevel describes the upgrade from Level to Level+1. NeedExp is
// the experience earned within this level; player talent experience itself is
// cumulative and is not reset by an upgrade.
type TalentGrowthLevel struct {
	Level   uint64
	NeedExp uint64
	Costs   []PromotionCost
}

type TalentUpgradeRule struct {
	CharacterID      uint64
	TalentID         uint64
	GrowthGroup      uint64
	CurrentLevel     uint64
	MaxLevel         uint64
	RequiredTotalExp uint64
	Costs            []PromotionCost
}

// TalentGrowthDesign joins CharTable -> TalentTable -> TalentGrowthTable so
// the runtime never guesses a growth group or hard-codes skill-book IDs.
type TalentGrowthDesign struct {
	Characters map[uint64]CharacterTalent
	Levels     map[[2]uint64]TalentGrowthLevel
}

func LoadTalentGrowthDesign(root, version string) (*TalentGrowthDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadTalentGrowthDesign(db)
}

func loadTalentGrowthDesign(db *sql.DB) (*TalentGrowthDesign, error) {
	if db == nil {
		return nil, errors.New("gamedata: nil talent growth database")
	}
	talents := make(map[uint64]CharacterTalent)
	rows, err := db.Query("SELECT id,ProtoBuf FROM TalentTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		groups, err := packedInts(raw, 6)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: talent %d growth group: %w", id, err)
		}
		maximum, err := packedInts(raw, 11)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: talent %d maximum level: %w", id, err)
		}
		if len(groups) != 1 || groups[0] == 0 || len(maximum) != 1 || maximum[0] == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: talent %d has invalid growth metadata", id)
		}
		talents[id] = CharacterTalent{TalentID: id, GrowthGroup: groups[0], MaxLevel: maximum[0]}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	design := &TalentGrowthDesign{
		Characters: make(map[uint64]CharacterTalent),
		Levels:     make(map[[2]uint64]TalentGrowthLevel),
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM CharTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var characterID uint64
		var raw []byte
		if err := rows.Scan(&characterID, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		talentIDs, err := packedInts(raw, 18)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character %d talent: %w", characterID, err)
		}
		if len(talentIDs) == 0 {
			continue
		}
		if len(talentIDs) != 1 || talentIDs[0] == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character %d has invalid talent reference", characterID)
		}
		talent, found := talents[talentIDs[0]]
		if !found {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character %d references unknown talent %d", characterID, talentIDs[0])
		}
		design.Characters[characterID] = talent
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM TalentGrowthTable ORDER BY groupId,id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var group, level uint64
		var raw []byte
		if err := rows.Scan(&group, &level, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		counts, countErr := packedInts(raw, 2)
		ids, idErr := packedInts(raw, 3)
		types, typeErr := packedInts(raw, 4)
		needs, needErr := packedInts(raw, 6)
		if countErr != nil || idErr != nil || typeErr != nil || needErr != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: talent growth %d/%d has invalid fields", group, level)
		}
		if group == 0 || level == 0 || len(counts) != len(ids) || len(ids) != len(types) || len(needs) > 1 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: talent growth %d/%d has mismatched fields", group, level)
		}
		entry := TalentGrowthLevel{Level: level}
		if len(needs) == 1 {
			entry.NeedExp = needs[0]
		}
		for i := range counts {
			if types[i] == 0 || counts[i] == 0 || (types[i] == 4 && ids[i] != 0) || (types[i] != 4 && ids[i] == 0) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: talent growth %d/%d has invalid cost", group, level)
			}
			entry.Costs = append(entry.Costs, PromotionCost{Type: types[i], ID: ids[i], Count: counts[i]})
		}
		key := [2]uint64{group, level}
		if _, exists := design.Levels[key]; exists {
			rows.Close()
			return nil, fmt.Errorf("gamedata: duplicate talent growth %d/%d", group, level)
		}
		design.Levels[key] = entry
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(design.Characters) == 0 || len(design.Levels) == 0 {
		return nil, errors.New("gamedata: empty talent growth design")
	}
	validated := make(map[uint64]bool)
	for _, talent := range design.Characters {
		if validated[talent.TalentID] {
			continue
		}
		validated[talent.TalentID] = true
		for level := uint64(1); level < talent.MaxLevel; level++ {
			entry, ok := design.Levels[[2]uint64{talent.GrowthGroup, level}]
			if !ok || entry.NeedExp == 0 || len(entry.Costs) == 0 {
				return nil, fmt.Errorf("gamedata: talent %d missing upgrade level %d", talent.TalentID, level)
			}
		}
	}
	return design, nil
}

// UpgradeRule returns the exact current-level requirement and cumulative
// experience threshold. It intentionally refuses max-level characters.
func (d *TalentGrowthDesign) UpgradeRule(characterID, currentLevel uint64) (TalentUpgradeRule, error) {
	if d == nil || characterID == 0 || currentLevel == 0 {
		return TalentUpgradeRule{}, errors.New("gamedata: invalid talent upgrade lookup")
	}
	talent, found := d.Characters[characterID]
	if !found {
		return TalentUpgradeRule{}, fmt.Errorf("gamedata: character %d has no talent growth design", characterID)
	}
	if currentLevel >= talent.MaxLevel {
		return TalentUpgradeRule{}, fmt.Errorf("gamedata: character %d talent is already at maximum level %d", characterID, talent.MaxLevel)
	}
	var required uint64
	for level := uint64(1); level <= currentLevel; level++ {
		entry, ok := d.Levels[[2]uint64{talent.GrowthGroup, level}]
		if !ok || entry.NeedExp == 0 {
			return TalentUpgradeRule{}, fmt.Errorf("gamedata: talent %d missing growth level %d", talent.TalentID, level)
		}
		if required > math.MaxUint64-entry.NeedExp {
			return TalentUpgradeRule{}, fmt.Errorf("gamedata: talent %d experience threshold overflows", talent.TalentID)
		}
		required += entry.NeedExp
	}
	current := d.Levels[[2]uint64{talent.GrowthGroup, currentLevel}]
	if len(current.Costs) == 0 {
		return TalentUpgradeRule{}, fmt.Errorf("gamedata: talent %d level %d has no upgrade costs", talent.TalentID, currentLevel)
	}
	return TalentUpgradeRule{
		CharacterID: characterID, TalentID: talent.TalentID, GrowthGroup: talent.GrowthGroup,
		CurrentLevel: currentLevel, MaxLevel: talent.MaxLevel, RequiredTotalExp: required,
		Costs: append([]PromotionCost(nil), current.Costs...),
	}, nil
}
