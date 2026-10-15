package inventory

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
)

func (s *EquipmentInventory) runEquipmentSequence(ctx command.Context, index, count, goldLimit, target uint64) (equipmentAutoBreakResult, error) {
	if s.upgrade == nil || s.wallet == nil || s.inventory == nil {
		return equipmentAutoBreakResult{}, errors.New("inventory: equipment upgrade unavailable")
	}
	at := s.equipmentPositionLocked(index)
	if at < 0 {
		return equipmentAutoBreakResult{}, errors.New("inventory: upgrade equipment not found")
	}
	if s.pendingReroll != nil && s.pendingReroll.Equipment.InvenIndex == index {
		return equipmentAutoBreakResult{}, errors.New("inventory: equipment option confirmation pending")
	}
	maximum := s.upgrade.MaxLevel[s.owned.Equipment[at].ID]
	if maximum == 0 {
		return equipmentAutoBreakResult{}, errors.New("inventory: equipment upgrade design missing")
	}
	if target == 0 || target > maximum {
		target = maximum
	}
	result := equipmentAutoBreakResult{Result: equipUpgradeStopMaxTryCount}
	for {
		entry := s.owned.Equipment[at]
		if entry.Level >= maximum {
			result.Result = equipUpgradeStopMaxLevel
			break
		}
		if entry.Level >= target {
			result.Result = equipUpgradeStopTargetLevel
			break
		}
		if result.Attempts >= count {
			break
		}
		level, _, err := s.upgrade.Level(entry.ID, entry.Level)
		if err != nil {
			return equipmentAutoBreakResult{}, err
		}
		materials, gold, err := s.selectUpgradeCosts(ctx, level.Costs)
		if err != nil || (gold != 0 && !s.wallet.CanSpendGold(gold)) {
			result.Result = equipUpgradeStopNotEnough
			result.Lack = upgradeLackItems(level.Costs)
			break
		}
		if goldLimit != 0 && (result.Gold > goldLimit || gold > goldLimit-result.Gold) {
			result.Result = equipUpgradeStopGoldLimit
			break
		}
		_, _, spent, actual, err := s.attemptUpgradeLocked(ctx, index, materials)
		if err != nil {
			return equipmentAutoBreakResult{}, err
		}
		result.Attempts++
		result.Gold += spent
		result.Consumed = append(result.Consumed, actual...)
	}
	result.Equipment = []Equipment{cloneEquipment(s.owned.Equipment[at])}
	result.Consumed = mergeRewardDeltas(result.Consumed)
	return result, nil
}

func (s *EquipmentInventory) fromMakingOrUpgrade(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	var index, character, recipe, count, upgradeSeq, goldLimit, target uint64
	countField := 3
	if path != "/EquipFromUpgradeToBreak" {
		countField = 5
	}
	if err := operationVarints(request, map[int]*uint64{countField: &count, countField + 1: &upgradeSeq, countField + 2: &goldLimit, countField + 3: &target}); err != nil {
		return 0, nil, true, err
	}
	if count > 100000 || upgradeSeq > math.MaxInt32 || goldLimit > math.MaxInt32 || target > math.MaxInt32 {
		return 0, nil, true, errors.New("inventory: invalid conversion upgrade limits")
	}
	var gain uint64
	if path == "/EquipFromUpgradeToBreak" {
		var found bool
		var err error
		index, found, err = wire.Varint(request, 2)
		if err != nil || !found || index == 0 {
			return 0, nil, true, errors.New("inventory: upgrade break equipment missing")
		}
		at := s.equipmentPositionLocked(index)
		if at < 0 || s.owned.Equipment[at].UseChar != 0 || s.owned.Equipment[at].LockFlag != 0 {
			return 0, nil, true, errors.New("inventory: assigned or locked equipment cannot be broken")
		}
	} else {
		if err := operationVarints(request, map[int]*uint64{2: &character, 3: &recipe}); err != nil {
			return 0, nil, true, err
		}
		if character == 0 || recipe == 0 {
			return 0, nil, true, errors.New("inventory: conversion making character or recipe missing")
		}
		materials, err := DecodeItemRequest(request, 4, path)
		if err != nil {
			return 0, nil, true, err
		}
		generated, experience, catalyst, maximum, err := s.prepareEquipmentMaking(ctx, character, recipe, 1, materials)
		if err != nil {
			return 0, nil, true, err
		}
		if len(generated) != 1 {
			return 0, nil, true, errors.New("inventory: single conversion recipe must produce one equipment")
		}
		if path == "/EquipFromMakingToUpgrade" {
			if s.capacity == nil {
				return 0, nil, true, errors.New("inventory: equipment capacity unavailable")
			}
			counts, err := s.capacity.InventorySlotCounts(ctx)
			if err != nil {
				return 0, nil, true, err
			}
			if uint64(len(s.owned.Equipment)) >= counts.Equipment {
				return 0, nil, true, errors.New("inventory: equipment capacity exceeded")
			}
		}
		if err := s.inventory.Consume(ctx, materials); err != nil {
			return 0, nil, true, err
		}
		identity := "equipment-conversion:" + ctx.SessionID + ":" + ctx.RequestID + ":" + path + ":" + strconv.FormatUint(seq, 10)
		if catalyst != 0 {
			if _, err := s.wallet.SpendCatalystOnce(ctx, identity, catalyst); err != nil {
				return 0, nil, true, err
			}
		}
		created, err := s.grantGeneratedBatch(ctx, "", generated, false)
		if err != nil {
			return 0, nil, true, err
		}
		index = created[0].InvenIndex
		if experience != 0 {
			if _, err := s.characters.AddEquipmentTalentExperience(ctx, character, experience, maximum); err != nil {
				return 0, nil, true, err
			}
		}
		gain = experience
	}
	result, err := s.runEquipmentSequence(ctx, index, count, goldLimit, target)
	if err != nil {
		return 0, nil, true, err
	}
	if path != "/EquipFromMakingToUpgrade" {
		entry := result.Equipment[0]
		rewards, err := s.upgrade.BreakRewards(entry.ID, entry.Level)
		if err != nil {
			return 0, nil, true, err
		}
		next := cloneEquipmentSnapshot(s.owned)
		at := s.equipmentPositionLocked(index)
		next.Equipment = slices.Delete(next.Equipment, at, at+1)
		if err := s.commitLocked(ctx, next, "conversion break"); err != nil {
			return 0, nil, true, err
		}
		identity := fmt.Sprintf("equipment-conversion-break:%s:%s:%s:%d", ctx.SessionID, ctx.RequestID, path, seq)
		result.Granted, err = s.inventory.GrantOnce(ctx, identity, rewards)
		if err != nil {
			return 0, nil, true, err
		}
		if result.Granted == nil {
			result.Granted = s.inventory.GrantedItems(identity)
		}
	}
	if path == "/EquipFromUpgradeToBreak" {
		return 385, encodeUpgradeBreakResponse(result, 1, 2, 3, 4, 5, 6, 7), true, nil
	}
	response := wire.AppendVarint(nil, 1, gain)
	response = append(response, encodeUpgradeBreakResponse(result, 2, 3, 4, 5, 6, 7, 8)...)
	code := 384
	if path == "/EquipFromMakingToUpgrade" {
		code = 386
	}
	return code, response, true, nil
}
