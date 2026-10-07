package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
)

const equipmentBatchLimit = 100

type equipmentAutoBreakResult struct {
	Equipment []Equipment
	Result    uint64
	Attempts  uint64
	Consumed  []Item
	Lack      []Item
	Gold      uint64
	Granted   []Item
}

func (s *EquipmentInventory) prepareEquipmentMaking(ctx command.Context, characterIndex, recipeID, count uint64, materials []Item) ([]Equipment, uint64, uint64, uint64, error) {
	if s.craft == nil || s.inventory == nil || s.characters == nil || s.wallet == nil {
		return nil, 0, 0, 0, errors.New("player: equipment making unavailable")
	}
	recipe, ok := s.craft.Recipe(recipeID)
	if !ok {
		return nil, 0, 0, 0, fmt.Errorf("player: unknown equipment making recipe %d", recipeID)
	}
	character, ok := s.characters.EquipmentCharacter(ctx, characterIndex)
	if !ok {
		return nil, 0, 0, 0, fmt.Errorf("player: unknown equipment making character %d", characterIndex)
	}
	gain, catalyst, maximum, err := s.craft.Talent(character.ID, character.TalentLevel, recipe.TalentLevel, count, character.TalentExp)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if catalyst != 0 && !s.wallet.CanSpendCatalyst(catalyst) {
		return nil, 0, 0, 0, errors.New("player: insufficient catalyst for equipment making")
	}
	if err := ValidateMakingMaterials(recipe.Costs, count, materials); err != nil {
		return nil, 0, 0, 0, err
	}
	if err := s.inventory.CanConsume(ctx, materials); err != nil {
		return nil, 0, 0, 0, err
	}
	if recipe.ResultCount != 0 && count > math.MaxUint64/recipe.ResultCount {
		return nil, 0, 0, 0, errors.New("player: equipment making result count overflow")
	}
	resultCount := count * recipe.ResultCount
	generated := make([]Equipment, 0, resultCount)
	for range resultCount {
		rolled, err := s.craft.Generate(recipeID)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("player: roll equipment making result: %w", err)
		}
		entry := Equipment{ID: rolled.Design.ID, Rank: []uint64{0, 0, 0}}
		for _, option := range rolled.Main {
			entry.MainOption = append(entry.MainOption, EquipmentOption{GroupID: option.GroupID, ID: option.ID})
		}
		for _, option := range rolled.Sub {
			entry.SubOption = append(entry.SubOption, EquipmentOption{GroupID: option.GroupID, ID: option.ID})
		}
		if rolled.Private != nil {
			entry.PrivateOption = &EquipmentOption{GroupID: rolled.Private.GroupID, ID: rolled.Private.ID}
		}
		generated = append(generated, entry)
	}
	return generated, gain, catalyst, maximum, nil
}

func ValidateMakingMaterials(costs []gamedata.PromotionCost, count uint64, materials []Item) error {
	want := make(map[[2]uint64]uint64, len(costs))
	for _, cost := range costs {
		if cost.Count == 0 || count > math.MaxUint64/cost.Count {
			return errors.New("player: equipment making material cost overflow")
		}
		key := [2]uint64{cost.Type, cost.ID}
		value := cost.Count * count
		if math.MaxUint64-want[key] < value {
			return errors.New("player: equipment making material cost overflow")
		}
		want[key] += value
	}
	got := make(map[[2]uint64]uint64, len(materials))
	for _, item := range materials {
		key := [2]uint64{item.Type, item.ID}
		if math.MaxUint64-got[key] < item.Count {
			return errors.New("player: equipment making material count overflow")
		}
		got[key] += item.Count
	}
	if len(got) != len(want) {
		return errors.New("player: equipment making material kinds mismatch")
	}
	for key, count := range want {
		if got[key] != count {
			return fmt.Errorf("player: equipment making material %d/%d=%d want=%d", key[0], key[1], got[key], count)
		}
	}
	return nil
}

func (s *EquipmentInventory) grantGeneratedBatch(ctx command.Context, identityPrefix string, entries []Equipment, trackReplay bool) ([]Equipment, error) {
	if len(entries) == 0 || (trackReplay && identityPrefix == "") {
		return nil, errors.New("player: invalid generated equipment batch")
	}

	if trackReplay {
		existing := make([]Equipment, 0, len(entries))
		found := 0
		for i := range entries {
			index := s.owned.Granted[identityPrefix+":"+strconv.Itoa(i)]
			if index == 0 {
				continue
			}
			found++
			position := s.equipmentPositionLocked(index)
			if position < 0 {
				return nil, errors.New("player: generated equipment replay index is missing")
			}
			existing = append(existing, cloneEquipment(s.owned.Equipment[position]))
		}
		if found != 0 {
			if found != len(entries) {
				return nil, errors.New("player: partial generated equipment replay batch")
			}
			return existing, nil
		}
	}
	next := cloneEquipmentSnapshot(s.owned)
	created := make([]Equipment, 0, len(entries))
	for i, source := range entries {
		entry := cloneEquipment(source)
		if entry.ID == 0 || len(entry.Rank) != 3 {
			return nil, errors.New("player: invalid generated equipment in batch")
		}
		entry.InvenIndex = next.NextIndex
		next.NextIndex++
		next.Equipment = append(next.Equipment, entry)
		created = append(created, cloneEquipment(entry))
		if trackReplay {
			next.Granted[identityPrefix+":"+strconv.Itoa(i)] = entry.InvenIndex
		}
	}
	if err := s.commitLocked(ctx, next, "generated equipment batch"); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *EquipmentInventory) runUpgradeToBreak(ctx command.Context, indices []uint64, target uint64, rewardIdentity string) (equipmentAutoBreakResult, error) {

	if s.upgrade == nil || s.wallet == nil || s.inventory == nil {

		return equipmentAutoBreakResult{}, errors.New("player: automatic equipment upgrade and break unavailable")
	}
	for _, index := range indices {
		position := s.equipmentPositionLocked(index)
		if position < 0 {

			return equipmentAutoBreakResult{}, fmt.Errorf("player: unknown equipment %d", index)
		}
		entry := s.owned.Equipment[position]
		if entry.UseChar != 0 || entry.LockFlag != 0 || !s.upgrade.CanBreak(entry.ID) {

			return equipmentAutoBreakResult{}, fmt.Errorf("player: equipment %d cannot be automatically broken", index)
		}
	}
	result := equipmentAutoBreakResult{Result: equipUpgradeStopTargetLevel}
	stopUpgrades := false
	for _, index := range indices {
		position := s.equipmentPositionLocked(index)
		entry := s.owned.Equipment[position]
		maximum := s.upgrade.MaxLevel[entry.ID]
		goal := min(target, maximum)
		for !stopUpgrades && entry.Level < goal {
			level, _, err := s.upgrade.Level(entry.ID, entry.Level)
			if err != nil {

				return equipmentAutoBreakResult{}, err
			}
			materials, gold, err := s.selectUpgradeCosts(ctx, level.Costs)
			if err != nil || (gold != 0 && !s.wallet.CanSpendGold(gold)) {
				result.Result = equipUpgradeStopNotEnough
				result.Lack = upgradeLackItems(level.Costs)
				stopUpgrades = true
				break
			}
			updated, _, spent, consumed, err := s.attemptUpgradeLocked(ctx, index, materials)
			if err != nil {

				return equipmentAutoBreakResult{}, err
			}
			result.Attempts++
			result.Gold += spent
			for _, item := range consumed {
				if item.Type != 4 {
					result.Consumed = append(result.Consumed, item)
				}
			}
			entry = updated
			if result.Attempts > 300000 {

				return equipmentAutoBreakResult{}, errors.New("player: automatic equipment upgrade exceeded safety limit")
			}
		}
		position = s.equipmentPositionLocked(index)
		result.Equipment = append(result.Equipment, cloneEquipment(s.owned.Equipment[position]))
	}
	rewards := make(map[[2]uint64]uint64)
	positions := make([]int, 0, len(indices))
	for _, entry := range result.Equipment {
		breakRewards, err := s.upgrade.BreakRewards(entry.ID, entry.Level)
		if err != nil {

			return equipmentAutoBreakResult{}, err
		}
		if err := addBattleRewards(rewards, breakRewards); err != nil {

			return equipmentAutoBreakResult{}, err
		}
		positions = append(positions, s.equipmentPositionLocked(entry.InvenIndex))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(positions)))
	next := cloneEquipmentSnapshot(s.owned)
	for _, position := range positions {
		next.Equipment = append(next.Equipment[:position], next.Equipment[position+1:]...)
	}
	if err := s.commitLocked(ctx, next, "automatic upgrade and break"); err != nil {

		return equipmentAutoBreakResult{}, err
	}

	granted, err := s.inventory.GrantOnce(ctx, rewardIdentity, aggregateBattleRewards(rewards))
	if err != nil {
		return equipmentAutoBreakResult{}, fmt.Errorf("player: grant automatic equipment break rewards: %w", err)
	}
	if granted == nil {
		granted = s.inventory.GrantedItems(rewardIdentity)
	}
	result.Granted = granted
	result.Consumed = aggregateItemCounts(result.Consumed)
	result.Lack = aggregateItemCounts(result.Lack)
	return result, nil
}

func addBattleRewards(total map[[2]uint64]uint64, rewards []gamedata.BattleReward) error {
	for _, reward := range rewards {
		key := [2]uint64{reward.Type, reward.ID}
		if reward.Count == 0 || math.MaxUint64-total[key] < reward.Count {
			return errors.New("player: equipment break reward overflow")
		}
		total[key] += reward.Count
	}
	return nil
}

func aggregateBattleRewards(values map[[2]uint64]uint64) []gamedata.BattleReward {
	keys := make([][2]uint64, 0, len(values))
	for key, count := range values {
		if count != 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	out := make([]gamedata.BattleReward, 0, len(keys))
	for _, key := range keys {
		out = append(out, gamedata.BattleReward{Type: key[0], ID: key[1], Count: values[key]})
	}
	return out
}

func aggregateItemCounts(items []Item) []Item {
	values := make(map[[2]uint64]uint64)
	for _, item := range items {
		values[[2]uint64{item.Type, item.ID}] += item.Count
	}
	keys := make([][2]uint64, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	out := make([]Item, 0, len(keys))
	for _, key := range keys {
		out = append(out, Item{Type: key[0], ID: key[1], Count: values[key]})
	}
	return out
}

func (s *EquipmentInventory) cachedEquipmentReply(key string) (smeltingReply, bool) {

	reply, ok := s.smeltCache[key]
	if ok {
		reply.body = append([]byte(nil), reply.body...)
	}
	return reply, ok
}

func (s *EquipmentInventory) cacheEquipmentReply(key string, code int, body []byte) {

	s.smeltCache[key] = smeltingReply{code: code, body: append([]byte(nil), body...)}
}
