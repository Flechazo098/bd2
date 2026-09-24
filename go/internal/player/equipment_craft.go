package player

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
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

func (s *EquipmentInventory) makeEquipment(request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	characterIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipMaking missing character")
	}
	recipeID, found, err := wire.Varint(request, 3)
	if err != nil || !found || recipeID == 0 {
		return 0, nil, true, errors.New("player: EquipMaking missing recipe")
	}
	count, found, err := wire.Varint(request, 4)
	if err != nil || !found || count == 0 || count > 1000 {
		return 0, nil, true, errors.New("player: EquipMaking invalid count")
	}
	materials, err := equipmentRequestItems(request, 5, "EquipMaking")
	if err != nil {
		return 0, nil, true, err
	}
	cacheKey := s.smeltingCacheKey("making", seq)
	if response, ok := s.cachedEquipmentReply(cacheKey); ok {
		return response.code, response.body, true, nil
	}
	generated, gain, catalyst, maximum, err := s.prepareEquipmentMaking(characterIndex, recipeID, count, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.inventory.Consume(materials); err != nil {
		return 0, nil, true, fmt.Errorf("player: consume equipment making materials: %w", err)
	}
	if catalyst != 0 {
		identity := "equip-making-catalyst:" + s.sessionID + ":" + strconv.FormatUint(seq, 10)
		if _, err := s.wallet.SpendCatalystOnce(identity, catalyst); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend equipment making catalyst: %w", err)
		}
	}
	created, err := s.grantGeneratedBatch("equip-making:"+s.sessionID+":"+strconv.FormatUint(seq, 10), generated, true)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: persist made equipment: %w", err)
	}
	if _, err := s.characters.AddTalentExperience(characterIndex, gain, maximum); err != nil {
		return 0, nil, true, err
	}
	var response []byte
	for _, entry := range created {
		response = wire.AppendBytes(response, 1, EquipmentWire(entry))
	}
	if gain != 0 {
		response = wire.AppendVarint(response, 2, gain)
	}
	s.cacheEquipmentReply(cacheKey, 50, response)
	return 50, response, true, nil
}

func (s *EquipmentInventory) prepareEquipmentMaking(characterIndex, recipeID, count uint64, materials []Item) ([]Equipment, uint64, uint64, uint64, error) {
	if s.craft == nil || s.inventory == nil || s.characters == nil || s.wallet == nil {
		return nil, 0, 0, 0, errors.New("player: equipment making unavailable")
	}
	recipe, ok := s.craft.Recipe(recipeID)
	if !ok {
		return nil, 0, 0, 0, fmt.Errorf("player: unknown equipment making recipe %d", recipeID)
	}
	character, ok := s.characters.Find(characterIndex)
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
	if err := validateMakingMaterials(recipe.Costs, count, materials); err != nil {
		return nil, 0, 0, 0, err
	}
	if err := s.inventory.CanConsume(materials); err != nil {
		return nil, 0, 0, 0, err
	}
	if recipe.ResultCount != 0 && count > math.MaxUint64/recipe.ResultCount {
		return nil, 0, 0, 0, errors.New("player: equipment making result count overflow")
	}
	resultCount := count * recipe.ResultCount
	generated := make([]Equipment, 0, resultCount)
	for i := uint64(0); i < resultCount; i++ {
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

func validateMakingMaterials(costs []gamedata.PromotionCost, count uint64, materials []Item) error {
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

func (s *EquipmentInventory) breakEquipment(request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	indices, err := repeatedEquipmentIndices(request, 2, equipmentBatchLimit)
	if err != nil {
		return 0, nil, true, err
	}
	cacheKey := s.smeltingCacheKey("break", seq)
	if response, ok := s.cachedEquipmentReply(cacheKey); ok {
		return response.code, response.body, true, nil
	}
	s.mu.Lock()
	if s.upgrade == nil || s.inventory == nil {
		s.mu.Unlock()
		return 0, nil, true, errors.New("player: equipment break unavailable")
	}
	rewards := make(map[[2]uint64]uint64)
	positions := make([]int, 0, len(indices))
	for _, index := range indices {
		position := s.equipmentPositionLocked(index)
		if position < 0 {
			s.mu.Unlock()
			return 0, nil, true, fmt.Errorf("player: EquipBreak unknown equipment %d", index)
		}
		entry := s.owned.Equipment[position]
		if entry.UseChar != 0 || entry.LockFlag != 0 || !s.upgrade.CanBreak(entry.ID) {
			s.mu.Unlock()
			return 0, nil, true, fmt.Errorf("player: equipment %d cannot be broken", index)
		}
		result, err := s.upgrade.BreakRewards(entry.ID, entry.Level)
		if err != nil {
			s.mu.Unlock()
			return 0, nil, true, err
		}
		if err := addBattleRewards(rewards, result); err != nil {
			s.mu.Unlock()
			return 0, nil, true, err
		}
		positions = append(positions, position)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(positions)))
	next := cloneEquipmentSnapshot(s.owned)
	for _, position := range positions {
		next.Equipment = append(next.Equipment[:position], next.Equipment[position+1:]...)
	}
	if err := s.commitLocked(next, "break"); err != nil {
		s.mu.Unlock()
		return 0, nil, true, err
	}
	s.mu.Unlock()
	identity := "equip-break:" + s.sessionID + ":" + strconv.FormatUint(seq, 10)
	granted, err := s.inventory.GrantOnce(identity, aggregateBattleRewards(rewards))
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: grant equipment break rewards: %w", err)
	}
	if granted == nil {
		granted = s.inventory.GrantedItems(identity)
	}
	response := wire.AppendBytes(nil, 1, rewardItemBundle(granted))
	s.cacheEquipmentReply(cacheKey, 56, response)
	return 56, response, true, nil
}

func (s *EquipmentInventory) upgradeToBreakAuto(request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	indices, err := repeatedEquipmentIndices(request, 2, equipmentBatchLimit)
	if err != nil {
		return 0, nil, true, err
	}
	target, _, err := wire.Varint(request, 3)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipUpgradeToBreakAuto invalid target")
	}
	cacheKey := s.smeltingCacheKey("upgrade-break-auto", seq)
	if response, ok := s.cachedEquipmentReply(cacheKey); ok {
		return response.code, response.body, true, nil
	}
	result, err := s.runUpgradeToBreak(indices, target, "equip-upgrade-break:"+s.sessionID+":"+strconv.FormatUint(seq, 10))
	if err != nil {
		return 0, nil, true, err
	}
	response := encodeUpgradeBreakResponse(result, 1, 2, 3, 4, 5, 6, 7)
	s.cacheEquipmentReply(cacheKey, 516, response)
	return 516, response, true, nil
}

func (s *EquipmentInventory) makeToBreakAuto(request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	characterIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipMakingToBreakAuto missing character")
	}
	recipeID, found, err := wire.Varint(request, 3)
	if err != nil || !found || recipeID == 0 {
		return 0, nil, true, errors.New("player: EquipMakingToBreakAuto missing recipe")
	}
	count, found, err := wire.Varint(request, 4)
	if err != nil || !found || count == 0 || count > equipmentBatchLimit {
		return 0, nil, true, errors.New("player: EquipMakingToBreakAuto invalid count")
	}
	materials, err := equipmentRequestItems(request, 5, "EquipMakingToBreakAuto")
	if err != nil {
		return 0, nil, true, err
	}
	target, _, err := wire.Varint(request, 6)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipMakingToBreakAuto invalid target")
	}
	cacheKey := s.smeltingCacheKey("making-break-auto", seq)
	if response, ok := s.cachedEquipmentReply(cacheKey); ok {
		return response.code, response.body, true, nil
	}
	generated, gain, catalyst, maximum, err := s.prepareEquipmentMaking(characterIndex, recipeID, count, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.inventory.Consume(materials); err != nil {
		return 0, nil, true, fmt.Errorf("player: consume equipment making materials: %w", err)
	}
	if catalyst != 0 {
		identity := "equip-making-break-catalyst:" + s.sessionID + ":" + strconv.FormatUint(seq, 10)
		if _, err := s.wallet.SpendCatalystOnce(identity, catalyst); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend automatic equipment making catalyst: %w", err)
		}
	}
	created, err := s.grantGeneratedBatch("", generated, false)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: persist auto-break made equipment: %w", err)
	}
	indices := make([]uint64, 0, len(created))
	for _, entry := range created {
		indices = append(indices, entry.InvenIndex)
	}
	if _, err := s.characters.AddTalentExperience(characterIndex, gain, maximum); err != nil {
		return 0, nil, true, err
	}
	result, err := s.runUpgradeToBreak(indices, target, "equip-making-break-reward:"+s.sessionID+":"+strconv.FormatUint(seq, 10))
	if err != nil {
		return 0, nil, true, err
	}
	var response []byte
	if gain != 0 {
		response = wire.AppendVarint(response, 1, gain)
	}
	response = append(response, encodeUpgradeBreakResponse(result, 2, 3, 4, 5, 6, 7, 8)...)
	s.cacheEquipmentReply(cacheKey, 515, response)
	return 515, response, true, nil
}

func (s *EquipmentInventory) grantGeneratedBatch(identityPrefix string, entries []Equipment, trackReplay bool) ([]Equipment, error) {
	if len(entries) == 0 || (trackReplay && identityPrefix == "") {
		return nil, errors.New("player: invalid generated equipment batch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if err := s.commitLocked(next, "generated equipment batch"); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *EquipmentInventory) runUpgradeToBreak(indices []uint64, target uint64, rewardIdentity string) (equipmentAutoBreakResult, error) {
	s.mu.Lock()
	if s.upgrade == nil || s.wallet == nil || s.inventory == nil {
		s.mu.Unlock()
		return equipmentAutoBreakResult{}, errors.New("player: automatic equipment upgrade and break unavailable")
	}
	for _, index := range indices {
		position := s.equipmentPositionLocked(index)
		if position < 0 {
			s.mu.Unlock()
			return equipmentAutoBreakResult{}, fmt.Errorf("player: unknown equipment %d", index)
		}
		entry := s.owned.Equipment[position]
		if entry.UseChar != 0 || entry.LockFlag != 0 || !s.upgrade.CanBreak(entry.ID) {
			s.mu.Unlock()
			return equipmentAutoBreakResult{}, fmt.Errorf("player: equipment %d cannot be automatically broken", index)
		}
	}
	result := equipmentAutoBreakResult{Result: equipUpgradeStopTargetLevel}
	stopUpgrades := false
	for _, index := range indices {
		position := s.equipmentPositionLocked(index)
		entry := s.owned.Equipment[position]
		maximum := s.upgrade.MaxLevel[entry.ID]
		goal := target
		if goal > maximum {
			goal = maximum
		}
		for !stopUpgrades && entry.Level < goal {
			level, _, err := s.upgrade.Level(entry.ID, entry.Level)
			if err != nil {
				s.mu.Unlock()
				return equipmentAutoBreakResult{}, err
			}
			materials, gold, err := s.selectUpgradeCosts(level.Costs)
			if err != nil || (gold != 0 && !s.wallet.CanSpendGold(gold)) {
				result.Result = equipUpgradeStopNotEnough
				result.Lack = upgradeLackItems(level.Costs)
				stopUpgrades = true
				break
			}
			updated, _, spent, consumed, err := s.attemptUpgradeLocked(index, materials)
			if err != nil {
				s.mu.Unlock()
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
				s.mu.Unlock()
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
			s.mu.Unlock()
			return equipmentAutoBreakResult{}, err
		}
		if err := addBattleRewards(rewards, breakRewards); err != nil {
			s.mu.Unlock()
			return equipmentAutoBreakResult{}, err
		}
		positions = append(positions, s.equipmentPositionLocked(entry.InvenIndex))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(positions)))
	next := cloneEquipmentSnapshot(s.owned)
	for _, position := range positions {
		next.Equipment = append(next.Equipment[:position], next.Equipment[position+1:]...)
	}
	if err := s.commitLocked(next, "automatic upgrade and break"); err != nil {
		s.mu.Unlock()
		return equipmentAutoBreakResult{}, err
	}
	s.mu.Unlock()
	granted, err := s.inventory.GrantOnce(rewardIdentity, aggregateBattleRewards(rewards))
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

func encodeUpgradeBreakResponse(result equipmentAutoBreakResult, equipmentField, resultField, attemptsField, consumedField, lackField, goldField, rewardsField int) []byte {
	var response []byte
	for _, entry := range result.Equipment {
		response = wire.AppendBytes(response, equipmentField, EquipmentWire(entry))
	}
	if result.Result != 0 {
		response = wire.AppendVarint(response, resultField, result.Result)
	}
	if result.Attempts != 0 {
		response = wire.AppendVarint(response, attemptsField, result.Attempts)
	}
	for _, item := range result.Consumed {
		response = wire.AppendBytes(response, consumedField, ItemWire(item))
	}
	for _, item := range result.Lack {
		response = wire.AppendBytes(response, lackField, ItemWire(item))
	}
	if result.Gold != 0 {
		response = wire.AppendVarint(response, goldField, result.Gold)
	}
	response = wire.AppendBytes(response, rewardsField, rewardItemBundle(result.Granted))
	return response
}

func repeatedEquipmentIndices(request []byte, number, maximum int) ([]uint64, error) {
	var result []uint64
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		values, err := decodeRepeatedUint64(field)
		if err != nil {
			return err
		}
		result = append(result, values...)
		return nil
	})
	if err != nil || len(result) == 0 || len(result) > maximum {
		return nil, errors.New("player: invalid equipment index batch")
	}
	seen := make(map[uint64]bool, len(result))
	for _, index := range result {
		if index == 0 || seen[index] {
			return nil, errors.New("player: invalid or duplicate equipment index")
		}
		seen[index] = true
	}
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

func rewardItemBundle(items []Item) []byte {
	var bundle []byte
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
	}
	return bundle
}

func (s *EquipmentInventory) cachedEquipmentReply(key string) (smeltingReply, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply, ok := s.smeltCache[key]
	if ok {
		reply.body = append([]byte(nil), reply.body...)
	}
	return reply, ok
}

func (s *EquipmentInventory) cacheEquipmentReply(key string, code int, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.smeltCache[key] = smeltingReply{code: code, body: append([]byte(nil), body...)}
}
