package player

import (
	"bd2server/internal/server/gamedata"
	"fmt"
	"math"
)

// Batch requests specify the missing final quantity and can supply existing
// intermediate resources plus raw resources. Only EquipmentResource recipes
// participate in this graph; conversion recipes would introduce cycles.
func (s *ItemCraftService) prepareAlchemyBatch(c Character, recipe gamedata.ItemCraftRecipe, count uint64, materials []Item) (gain, catalyst, maximum uint64, err error) {
	if recipe.Category != 1 || recipe.Result.Count == 0 || count%recipe.Result.Count != 0 {
		return 0, 0, 0, fmt.Errorf("craft: invalid batch output")
	}
	available := map[[2]uint64]uint64{}
	for _, item := range materials {
		key := [2]uint64{item.Type, item.ID}
		if item.Count > math.MaxInt32-available[key] {
			return 0, 0, 0, fmt.Errorf("craft: batch material overflow")
		}
		available[key] += item.Count
	}
	producers := map[[2]uint64]gamedata.ItemCraftRecipe{}
	for _, r := range s.design.Alchemy {
		if r.Category == 1 {
			key := [2]uint64{r.Result.Type, r.Result.ID}
			if _, ok := producers[key]; ok {
				return 0, 0, 0, fmt.Errorf("craft: ambiguous batch producer")
			}
			producers[key] = r
		}
	}
	visiting := map[uint64]bool{}
	var makeRecipe func(gamedata.ItemCraftRecipe, uint64) error
	makeRecipe = func(r gamedata.ItemCraftRecipe, n uint64) error {
		if visiting[r.ID] {
			return fmt.Errorf("craft: cyclic batch recipe")
		}
		visiting[r.ID] = true
		defer delete(visiting, r.ID)
		g, cost, max, e := s.talents.CraftTalent(c.ID, c.TalentLevel, 8, r.TalentLevel, n, c.TalentExp)
		if e != nil {
			return e
		}
		if gain > math.MaxInt32-g || catalyst > math.MaxInt32-cost {
			return fmt.Errorf("craft: batch growth overflow")
		}
		gain += g
		catalyst += cost
		maximum = max
		for _, material := range r.Costs {
			if material.Count == 0 || n > math.MaxInt32/material.Count {
				return fmt.Errorf("craft: batch quantity overflow")
			}
			need := material.Count * n
			key := [2]uint64{material.Type, material.ID}
			supplied := min(available[key], need)
			available[key] -= supplied
			need -= supplied
			if need == 0 {
				continue
			}
			lower, ok := producers[key]
			if !ok || lower.Result.Count == 0 || need%lower.Result.Count != 0 {
				return fmt.Errorf("craft: missing batch material %d/%d", key[0], key[1])
			}
			if e = makeRecipe(lower, need/lower.Result.Count); e != nil {
				return e
			}
		}
		return nil
	}
	if err = makeRecipe(recipe, count/recipe.Result.Count); err != nil {
		return 0, 0, 0, err
	}
	for _, n := range available {
		if n != 0 {
			return 0, 0, 0, fmt.Errorf("craft: extra batch materials")
		}
	}
	if c.TalentExp >= maximum {
		gain = 0
	} else {
		gain = min(gain, maximum-c.TalentExp)
	}
	return gain, catalyst, maximum, nil
}
