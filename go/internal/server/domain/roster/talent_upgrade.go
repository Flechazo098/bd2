package roster

import (
	"bd2server/internal/server/design/gamedata"

	assets "bd2server/internal/server/domain/inventory"
	"errors"
	"fmt"
	"math"
)

const talentSkillUpgradePacketCode = 44

func validateTalentUpgradeMaterials(costs []gamedata.PromotionCost, materials []assets.Item) ([]assets.Item, uint64, error) {
	want := make(map[[2]uint64]uint64, len(costs))
	for _, cost := range costs {
		if cost.Type == 0 || cost.Count == 0 || (cost.Type == 4 && cost.ID != 0) || (cost.Type != 4 && cost.ID == 0) {
			return nil, 0, errors.New("player: invalid GameData talent upgrade cost")
		}
		key := [2]uint64{cost.Type, cost.ID}
		if want[key] > math.MaxUint64-cost.Count {
			return nil, 0, errors.New("player: talent upgrade GameData cost overflow")
		}
		want[key] += cost.Count
	}
	got := make(map[[2]uint64]uint64, len(materials))
	var items []assets.Item
	var gold uint64
	for _, material := range materials {
		key := [2]uint64{material.Type, material.ID}
		if got[key] > math.MaxUint64-material.Count {
			return nil, 0, errors.New("player: talent upgrade material overflow")
		}
		got[key] += material.Count
		if material.Type == 4 {
			if gold != 0 || material.InvenIndex != 0 || material.ID != 0 {
				return nil, 0, errors.New("player: invalid talent upgrade currency")
			}
			gold = material.Count
		} else {
			items = append(items, material)
		}
	}
	if len(got) != len(want) {
		return nil, 0, errors.New("player: talent upgrade material kinds mismatch")
	}
	for key, count := range want {
		if got[key] != count {
			return nil, 0, fmt.Errorf("player: talent upgrade material %d/%d=%d want=%d", key[0], key[1], got[key], count)
		}
	}
	return items, gold, nil
}
