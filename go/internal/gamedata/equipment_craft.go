package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

const equipmentMakingTalentClass = 9

type EquipmentCraftRecipe struct {
	ID          uint64
	ResultCount uint64
	TalentLevel uint64
	Costs       []PromotionCost
	Results     []WeightedEquipment
}

type equipmentMakingTalent struct {
	SkillGroup  uint64
	GrowthGroup uint64
}

type equipmentMakingTalentSkill struct {
	Experience uint64
	Catalyst   uint64
}

type GeneratedEquipment struct {
	Design  EquipmentDesign
	Main    []EquipmentOptionChoice
	Sub     []EquipmentOptionChoice
	Private *EquipmentOptionChoice
}

type EquipmentCraftDesign struct {
	Recipes    map[uint64]EquipmentCraftRecipe
	Equipment  map[uint64]EquipmentDesign
	CharTalent map[uint64]equipmentMakingTalent
	Skills     map[[2]uint64]equipmentMakingTalentSkill
	TalentNeed map[[2]uint64]uint64
	draw       func(uint64) (uint64, error)
}

func LoadEquipmentCraftDesign(root, version string) (*EquipmentCraftDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadEquipmentCraftDesign(db)
}

func loadEquipmentCraftDesign(db *sql.DB) (*EquipmentCraftDesign, error) {
	design := &EquipmentCraftDesign{
		Recipes: make(map[uint64]EquipmentCraftRecipe), Equipment: make(map[uint64]EquipmentDesign),
		CharTalent: make(map[uint64]equipmentMakingTalent), Skills: make(map[[2]uint64]equipmentMakingTalentSkill),
		TalentNeed: make(map[[2]uint64]uint64), draw: cryptoDraw,
	}
	rows, err := db.Query("SELECT id,ProtoBuf FROM EquipmentMakingTable ORDER BY id")
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
		counts, _ := packedInts(raw, 4)
		materialIDs, _ := packedInts(raw, 5)
		materialTypes, _ := packedInts(raw, 6)
		resultCounts, _ := packedInts(raw, 7)
		boxIDs, _ := packedInts(raw, 8)
		resultTypes, _ := packedInts(raw, 9)
		talentLevels, _ := packedInts(raw, 11)
		if len(counts) == 0 || len(counts) != len(materialIDs) || len(counts) != len(materialTypes) ||
			len(resultCounts) != 1 || resultCounts[0] != 1 || len(boxIDs) != 1 ||
			len(resultTypes) != 1 || resultTypes[0] != 9 || len(talentLevels) != 1 || talentLevels[0] == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment making recipe %d malformed", id)
		}
		var box []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM RandomBoxTable WHERE id=?", boxIDs[0]).Scan(&box); err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment making recipe %d random box: %w", id, err)
		}
		groups, _ := packedInts(box, 9)
		if len(groups) != 1 || groups[0] == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment making random box %d malformed", boxIDs[0])
		}
		results, equipmentOnly, err := classifyEquipmentRewardPool(db, groups[0])
		if err != nil || !equipmentOnly || len(results) == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment making recipe %d reward group: %w", id, err)
		}
		recipe := EquipmentCraftRecipe{ID: id, ResultCount: resultCounts[0], TalentLevel: talentLevels[0], Results: results}
		for i := range counts {
			if counts[i] == 0 || materialIDs[i] == 0 || materialTypes[i] == 0 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: equipment making recipe %d invalid material", id)
			}
			recipe.Costs = append(recipe.Costs, PromotionCost{Type: materialTypes[i], ID: materialIDs[i], Count: counts[i]})
		}
		if err := design.loadEquipmentPool(db, results); err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment making recipe %d: %w", id, err)
		}
		design.Recipes[id] = recipe
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	talents := make(map[uint64]equipmentMakingTalent)
	rows, err = db.Query("SELECT id,ProtoBuf FROM TalentTable")
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
		classes, _ := packedInts(raw, 4)
		growthGroups, _ := packedInts(raw, 6)
		skillGroups, _ := packedInts(raw, 18)
		if len(classes) == 1 && classes[0] == equipmentMakingTalentClass && len(growthGroups) == 1 && len(skillGroups) == 1 {
			talents[id] = equipmentMakingTalent{SkillGroup: skillGroups[0], GrowthGroup: growthGroups[0]}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM CharTable")
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
		talentIDs, _ := packedInts(raw, 18)
		if len(talentIDs) == 1 {
			if talent, ok := talents[talentIDs[0]]; ok {
				design.CharTalent[id] = talent
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM TalentSkillTable WHERE classType=?", equipmentMakingTalentClass)
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
		catalysts, _ := packedInts(raw, 1)
		experience, _ := packedInts(raw, 5)
		if len(catalysts) > 1 || len(experience) > 1 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: equipment making talent skill %d/%d malformed", group, level)
		}
		var skill equipmentMakingTalentSkill
		if len(experience) == 1 {
			skill.Experience = experience[0]
		}
		if len(catalysts) == 1 {
			skill.Catalyst = catalysts[0]
		}
		design.Skills[[2]uint64{group, level}] = skill
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM TalentGrowthTable")
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
		need, _ := packedInts(raw, 6)
		if len(need) == 1 {
			design.TalentNeed[[2]uint64{group, level}] = need[0]
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return design, nil
}

func (d *EquipmentCraftDesign) loadEquipmentPool(db *sql.DB, pool []WeightedEquipment) error {
	for _, result := range pool {
		if result.ID == 0 {
			if len(result.Children) == 0 {
				return fmt.Errorf("empty nested equipment reward")
			}
			if err := d.loadEquipmentPool(db, result.Children); err != nil {
				return err
			}
			continue
		}
		if len(result.Children) != 0 {
			return fmt.Errorf("equipment reward %d also has children", result.ID)
		}
		if _, exists := d.Equipment[result.ID]; exists {
			continue
		}
		equipment, err := loadEquipmentDesign(db, result.ID)
		if err != nil {
			return err
		}
		d.Equipment[result.ID] = equipment
	}
	return nil
}

func (d *EquipmentCraftDesign) Recipe(id uint64) (EquipmentCraftRecipe, bool) {
	if d == nil {
		return EquipmentCraftRecipe{}, false
	}
	recipe, ok := d.Recipes[id]
	return recipe, ok
}

func (d *EquipmentCraftDesign) Generate(recipeID uint64) (GeneratedEquipment, error) {
	if d == nil {
		return GeneratedEquipment{}, fmt.Errorf("gamedata: equipment crafting unavailable")
	}
	recipe, ok := d.Recipes[recipeID]
	if !ok {
		return GeneratedEquipment{}, fmt.Errorf("gamedata: unknown equipment making recipe %d", recipeID)
	}
	draw := d.draw
	if draw == nil {
		draw = cryptoDraw
	}
	id, err := rollEquipmentChoiceWith(recipe.Results, draw)
	if err != nil {
		return GeneratedEquipment{}, err
	}
	equipment, ok := d.Equipment[id]
	if !ok {
		return GeneratedEquipment{}, fmt.Errorf("gamedata: equipment making result %d missing", id)
	}
	main, sub, private, err := rollEquipmentOptionsWith(equipment, draw)
	if err != nil {
		return GeneratedEquipment{}, err
	}
	return GeneratedEquipment{Design: equipment, Main: main, Sub: sub, Private: private}, nil
}

func rollEquipmentOptionsWith(design EquipmentDesign, draw func(uint64) (uint64, error)) (main, sub []EquipmentOptionChoice, private *EquipmentOptionChoice, err error) {
	pick := func(groups []OptionGroup) ([]EquipmentOptionChoice, error) {
		out := make([]EquipmentOptionChoice, 0, len(groups))
		for _, group := range groups {
			pool := make([]WeightedEquipment, 0, len(group.Choices))
			for _, choice := range group.Choices {
				pool = append(pool, WeightedEquipment{ID: choice.ID, Weight: choice.Weight})
			}
			id, err := rollEquipmentChoiceWith(pool, draw)
			if err != nil {
				return nil, err
			}
			out = append(out, EquipmentOptionChoice{GroupID: group.ID, ID: id})
		}
		return out, nil
	}
	if main, err = pick(design.Main); err != nil {
		return nil, nil, nil, err
	}
	if sub, err = pick(design.Sub); err != nil {
		return nil, nil, nil, err
	}
	choices, err := pick(design.Private)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(choices) > 1 {
		return nil, nil, nil, fmt.Errorf("gamedata: equipment %d has %d private option groups", design.ID, len(choices))
	}
	if len(choices) == 1 {
		choice := choices[0]
		private = &choice
	}
	return main, sub, private, nil
}

func (d *EquipmentCraftDesign) Talent(characterID, characterLevel, recipeLevel, count, currentExperience uint64) (gain, catalyst, maximum uint64, err error) {
	if d == nil || characterID == 0 || characterLevel == 0 || recipeLevel == 0 || count == 0 {
		return 0, 0, 0, fmt.Errorf("gamedata: invalid equipment making talent request")
	}
	talent, ok := d.CharTalent[characterID]
	if !ok {
		return 0, 0, 0, fmt.Errorf("gamedata: character %d has no equipment making talent", characterID)
	}
	if characterLevel < recipeLevel {
		return 0, 0, 0, fmt.Errorf("gamedata: equipment making talent level %d is below recipe level %d", characterLevel, recipeLevel)
	}
	recipeSkill, ok := d.Skills[[2]uint64{talent.SkillGroup, recipeLevel}]
	if !ok {
		return 0, 0, 0, fmt.Errorf("gamedata: equipment making talent skill %d/%d missing", talent.SkillGroup, recipeLevel)
	}
	currentSkill, ok := d.Skills[[2]uint64{talent.SkillGroup, characterLevel}]
	if !ok {
		return 0, 0, 0, fmt.Errorf("gamedata: equipment making talent skill %d/%d missing", talent.SkillGroup, characterLevel)
	}
	if currentSkill.Experience != 0 && count > math.MaxUint64/currentSkill.Experience {
		return 0, 0, 0, fmt.Errorf("gamedata: equipment making talent experience overflow")
	}
	if recipeSkill.Catalyst != 0 && count > math.MaxUint64/recipeSkill.Catalyst {
		return 0, 0, 0, fmt.Errorf("gamedata: equipment making catalyst overflow")
	}
	for level := uint64(1); level <= characterLevel; level++ {
		need, exists := d.TalentNeed[[2]uint64{talent.GrowthGroup, level}]
		if !exists {
			return 0, 0, 0, fmt.Errorf("gamedata: equipment making talent growth %d/%d missing", talent.GrowthGroup, level)
		}
		if maximum > math.MaxUint64-need {
			return 0, 0, 0, fmt.Errorf("gamedata: equipment making talent threshold overflow")
		}
		maximum += need
	}
	wanted := currentSkill.Experience * count
	if currentExperience >= maximum {
		wanted = 0
	} else if wanted > maximum-currentExperience {
		wanted = maximum - currentExperience
	}
	return wanted, recipeSkill.Catalyst * count, maximum, nil
}
