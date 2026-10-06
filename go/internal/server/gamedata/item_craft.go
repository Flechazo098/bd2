package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

type ItemCraftRecipe struct {
	ID, Class, TalentLevel, Category uint64
	Result                           BattleReward
	Costs                            []PromotionCost
}
type ItemCraftDesign struct{ Cooking, Alchemy map[uint64]ItemCraftRecipe }

func LoadItemCraftDesign(root, version string) (*ItemCraftDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	return loadItemCraftDesign(db)
}
func loadItemCraftDesign(db *sql.DB) (*ItemCraftDesign, error) {
	d := &ItemCraftDesign{Cooking: map[uint64]ItemCraftRecipe{}, Alchemy: map[uint64]ItemCraftRecipe{}}
	for _, name := range []string{"CookingTable", "AlchemyTable"} {
		rows, err := db.Query("SELECT id,ProtoBuf FROM " + name + " ORDER BY id")
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id uint64
			var raw []byte
			if err = rows.Scan(&id, &raw); err != nil {
				_ = rows.Close()
				return nil, err
			}
			r := ItemCraftRecipe{ID: id, Class: 8}
			if name == "AlchemyTable" {
				r.Category, err = optionalScalar(raw, 1)
				if err != nil || (r.Category != 1 && r.Category != 2) {
					return nil, fmt.Errorf("gamedata: invalid alchemy category %d", id)
				}
			}
			countField, idField, typeField, levelField, materialCount, materialID, materialType := 9, 10, 11, 12, 5, 6, 7
			if name == "CookingTable" {
				r.Class = 7
				countField, idField, typeField, levelField, materialCount, materialID, materialType = 12, 13, 0, 15, 7, 8, 0
				r.Result.Type = 5
			}
			for f, p := range map[int]*uint64{countField: &r.Result.Count, idField: &r.Result.ID, levelField: &r.TalentLevel} {
				*p, err = optionalScalar(raw, f)
				if err != nil {
					_ = rows.Close()
					return nil, err
				}
			}
			if typeField != 0 {
				r.Result.Type, err = optionalScalar(raw, typeField)
				if err != nil {
					_ = rows.Close()
					return nil, err
				}
			}
			ns, e1 := packedInts(raw, materialCount)
			ids, e2 := packedInts(raw, materialID)
			var ts []uint64
			var e3 error
			if materialType != 0 {
				ts, e3 = packedInts(raw, materialType)
			} else {
				for range ids {
					ts = append(ts, 5)
				}
			}
			if e1 != nil || e2 != nil || e3 != nil || len(ns) == 0 || len(ns) != len(ids) || len(ts) != len(ids) || id == 0 || id > math.MaxInt32 || r.Result.ID == 0 || r.Result.Count == 0 || r.Result.Count > math.MaxInt32 || r.TalentLevel == 0 {
				return nil, fmt.Errorf("gamedata: malformed %s recipe %d", name, id)
			}
			for i, n := range ns {
				if n == 0 || n > math.MaxInt32 || ids[i] == 0 || ids[i] > math.MaxInt32 || ts[i] == 0 {
					return nil, fmt.Errorf("gamedata: invalid recipe materials")
				}
				r.Costs = append(r.Costs, PromotionCost{Type: ts[i], ID: ids[i], Count: n})
			}
			if r.Class == 7 {
				d.Cooking[id] = r
			} else {
				d.Alchemy[id] = r
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

// CraftTalent validates the producer and recipe against the same talent and
// growth catalog used by ordinary field skills. Recipe level sets cost; the
// producer's current level sets experience, as in the client craft UI.
func (d *TalentUseDesign) CraftTalent(characterID, level, class, recipeLevel, count, experience uint64) (gain, catalyst, maximum uint64, err error) {
	c, ok := d.Characters[characterID]
	if !ok || level == 0 || level > c.MaxLevel || recipeLevel == 0 || level < recipeLevel || count == 0 || count > math.MaxInt32 {
		return 0, 0, 0, fmt.Errorf("gamedata: unavailable crafting talent")
	}
	current, ok := d.Rules[[2]uint64{c.Group, level}]
	if !ok || current.Class != class {
		return 0, 0, 0, fmt.Errorf("gamedata: wrong crafting talent class")
	}
	recipe, ok := d.Rules[[2]uint64{c.Group, recipeLevel}]
	if !ok {
		return 0, 0, 0, fmt.Errorf("gamedata: missing recipe talent level")
	}
	if d.Growth == nil {
		return 0, 0, 0, fmt.Errorf("gamedata: missing talent growth")
	}
	g, ok := d.Growth.Characters[characterID]
	if !ok {
		return 0, 0, 0, fmt.Errorf("gamedata: missing craft growth group")
	}
	for l := uint64(1); l <= level; l++ {
		entry, exists := d.Growth.Levels[[2]uint64{g.GrowthGroup, l}]
		if !exists {
			return 0, 0, 0, fmt.Errorf("gamedata: missing growth level")
		}
		if entry.NeedExp > math.MaxUint64-maximum {
			return 0, 0, 0, fmt.Errorf("gamedata: craft growth overflow")
		}
		maximum += entry.NeedExp
	}
	if recipe.Catalyst > math.MaxInt32/count || current.Experience > math.MaxInt32/count {
		return 0, 0, 0, fmt.Errorf("gamedata: crafting quantity overflow")
	}
	gain = current.Experience * count
	if experience >= maximum {
		gain = 0
	} else if gain > maximum-experience {
		gain = maximum - experience
	}
	return gain, recipe.Catalyst * count, maximum, nil
}
