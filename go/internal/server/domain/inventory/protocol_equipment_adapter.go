package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/ownership"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func EquipmentWire(v Equipment) []byte {
	out := ownership.Equipment{InvenIndex: v.InvenIndex, ID: v.ID, Level: v.Level, UseChar: v.UseChar, KeepFlag: v.KeepFlag, LockFlag: v.LockFlag, SortID: v.SortID, Mark: v.Mark, Rank: v.Rank}
	for _, o := range v.MainOption {
		out.MainOption = append(out.MainOption, ownership.EquipmentOption{GroupID: o.GroupID, ID: o.ID})
	}
	for _, o := range v.SubOption {
		out.SubOption = append(out.SubOption, ownership.EquipmentOption{GroupID: o.GroupID, ID: o.ID})
	}
	if v.PrivateOption != nil {
		out.PrivateOption = &ownership.EquipmentOption{GroupID: v.PrivateOption.GroupID, ID: v.PrivateOption.ID}
	}
	return ownership.EncodeEquipment(out)
}

func equipmentOptionWire(v EquipmentOption) []byte {
	return ownership.EncodeEquipmentOption(ownership.EquipmentOption{GroupID: v.GroupID, ID: v.ID})
}

func (s *EquipmentInventory) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/EquipInfo" && path != "/EquipUse" && path != "/EquipClear" && path != "/EquipChange" && path != "/EquipBatchUse" && path != "/EquipPresetInfo" && path != "/EquipPresetSave" && path != "/EquipPresetNameChange" && path != "/EquipUpgrade" && path != "/EquipSequenceUpgrade" && path != "/EquipSmelting" && path != "/EquipSequenceSmelting" && path != "/EquipOptionReRoll" && path != "/EquipOptionReRollConfirm" && path != "/EquipMainOptChange" && path != "/EquipMarkSet" && path != "/EquipMarkDelete" && path != "/EquipLock" && path != "/EquipMaking" && path != "/EquipBreak" && path != "/EquipMakingToBreakAuto" && path != "/EquipUpgradeToBreakAuto" {
		return 0, nil, false, nil
	}
	if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("player: %s invalid sequence", path)
	}
	if path == "/EquipUse" {
		return s.use(ctx, request)
	}
	if path == "/EquipClear" {
		return s.clear(ctx, request)
	}
	if path == "/EquipBatchUse" {
		return s.batchUse(ctx, request)
	}
	if path == "/EquipPresetInfo" {
		return s.presetInfo()
	}
	if path == "/EquipPresetSave" {
		return s.presetSave(ctx, request)
	}
	if path == "/EquipPresetNameChange" {
		return s.presetNameChange(ctx, request)
	}
	if path == "/EquipUpgrade" {
		return s.upgradeOnce(ctx, request)
	}
	if path == "/EquipSequenceUpgrade" {
		return s.upgradeSequence(ctx, request)
	}
	if path == "/EquipMaking" {
		return s.makeEquipment(ctx, request)
	}
	if path == "/EquipBreak" {
		return s.breakEquipment(ctx, request)
	}
	if path == "/EquipMakingToBreakAuto" {
		return s.makeToBreakAuto(ctx, request)
	}
	if path == "/EquipUpgradeToBreakAuto" {
		return s.upgradeToBreakAuto(ctx, request)
	}
	if path == "/EquipSmelting" {
		return s.smeltOnce(ctx, request)
	}
	if path == "/EquipSequenceSmelting" {
		return s.smeltSequence(ctx, request)
	}
	if path == "/EquipOptionReRoll" {
		return s.optionRerollRequest(ctx, request)
	}
	if path == "/EquipOptionReRollConfirm" {
		return s.optionRerollConfirm(ctx, request)
	}
	if path == "/EquipMainOptChange" {
		return s.mainOptionChange(ctx, request)
	}
	if path == "/EquipChange" {
		return s.change(ctx, request)
	}
	if path == "/EquipMarkSet" || path == "/EquipMarkDelete" {
		return s.mark(ctx, path, request)
	}
	if path == "/EquipLock" {
		return s.lock(ctx, request)
	}

	var response []byte
	for _, entry := range s.owned.Equipment {
		response = wire.AppendBytes(response, 1, EquipmentWire(entry))
	}
	if s.pendingReroll != nil {
		response = wire.AppendBytes(response, 2, EquipmentWire(s.pendingReroll.Equipment))
	}
	return 34, response, true, nil
}

// ApplyPresetEquipment applies the same authoritative state transition as
// /EquipBatchUse without fabricating a second wire protocol implementation.
func (s *EquipmentInventory) ApplyPresetEquipment(ctx command.Context, bindings []PresetEquipmentBinding) ([]EquipmentCharacter, error) {
	if err := s.ValidatePresetEquipment(ctx, bindings); err != nil {
		return nil, err
	}
	// PresetUse is a server-side atomic operation. Unlike the client-orchestrated
	// EquipPreset UI, it must also clear any previous owner of an equipment
	// instance in the same batch.

	requested := make(map[uint64]bool, len(bindings))
	desired := make(map[uint64]bool)
	for _, binding := range bindings {
		requested[binding.CharacterIndex] = true
		for _, index := range binding.Equipment {
			desired[index] = index != 0
		}
	}
	additional := make(map[uint64][]uint64)
	for _, item := range s.owned.Equipment {
		if item.UseChar == 0 || requested[item.UseChar] || !desired[item.InvenIndex] {
			continue
		}
		if additional[item.UseChar] == nil {
			additional[item.UseChar] = make([]uint64, equipmentSlotCount)
			for _, equipped := range s.owned.Equipment {
				if equipped.UseChar == item.UseChar {
					if slot, ok := s.slots[equipped.ID]; ok && slot < equipmentSlotCount {
						additional[item.UseChar][slot] = equipped.InvenIndex
					}
				}
			}
		}
		if slot, ok := s.slots[item.ID]; ok && slot < equipmentSlotCount {
			additional[item.UseChar][slot] = 0
		}
	}

	for character, equipment := range additional {
		bindings = append(bindings, PresetEquipmentBinding{CharacterIndex: character, Equipment: equipment})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].CharacterIndex < bindings[j].CharacterIndex })
	request := wire.AppendVarint(nil, 1, 1)
	for _, binding := range bindings {
		entry := wire.AppendVarint(nil, 1, binding.CharacterIndex)
		for _, index := range binding.Equipment {
			entry = wire.AppendVarint(entry, 2, index)
		}
		request = wire.AppendBytes(request, 2, entry)
	}
	_, response, handled, err := s.batchUse(ctx, request)
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, errors.New("player: preset equipment batch was not handled")
	}
	var result []EquipmentCharacter
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number != 1 || field.Type != 2 {
			return nil
		}
		index, found, err := wire.Varint(field.Value, 1)
		if err != nil || !found || index == 0 {
			return errors.New("player: malformed preset equipment character response")
		}
		character, found := s.characters.EquipmentCharacter(ctx, index)
		if !found {
			return fmt.Errorf("player: preset equipment character %d disappeared", index)
		}
		result = append(result, character)
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

// StatContributions resolves only maximum-health equipment effects. Snapshot
// options under the equipment lock and release it before calculating; callers
// may themselves be the CharacterStore maximum-health callback.
func (s *EquipmentInventory) StatContributions(ctx command.Context, characterIndex uint64) ([]gamedata.StatContribution, error) {
	if characterIndex == 0 {
		return nil, errors.New("player: invalid equipment stat character")
	}
	type query struct {
		option gamedata.EquipmentOption
		sub    bool
	}
	var queries []query

	design := s.statDesign
	for _, equipment := range s.owned.Equipment {
		if equipment.UseChar != characterIndex {
			continue
		}
		if equipment.Level > math.MaxInt32 {

			return nil, fmt.Errorf("player: invalid equipment %d level", equipment.InvenIndex)
		}
		var rank [3]int
		for i, value := range equipment.Rank {
			if i >= 3 || value > 4 {

				return nil, fmt.Errorf("player: invalid equipment %d ranks", equipment.InvenIndex)
			}
			rank[i] = int(value)
		}
		add := func(option EquipmentOption, sub bool) {
			if option.ID == 1 || option.ID == 2 {
				queries = append(queries, query{gamedata.EquipmentOption{GroupID: option.GroupID, ID: option.ID, Level: int(equipment.Level), Rank: rank}, sub})
			}
		}
		for _, option := range equipment.MainOption {
			add(option, false)
		}
		for _, option := range equipment.SubOption {
			add(option, true)
		}
		if equipment.PrivateOption != nil {
			add(*equipment.PrivateOption, false)
		}
	}

	if len(queries) == 0 {
		return nil, nil
	}
	if design == nil {
		return nil, errors.New("player: equipment stat design unavailable")
	}
	result := make([]gamedata.StatContribution, 0, len(queries))
	for _, query := range queries {
		contribution, err := design.HealthContribution(query.option, query.sub)
		if err != nil {
			return nil, err
		}
		result = append(result, contribution)
	}
	return result, nil
}

func (s *EquipmentInventory) makeEquipment(ctx command.Context, request []byte) (int, []byte, bool, error) {
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
	materials, err := DecodeItemRequest(request, 5, "EquipMaking")
	if err != nil {
		return 0, nil, true, err
	}
	cacheKey := s.smeltingCacheKey(ctx, "making", seq)
	if response, ok := s.cachedEquipmentReply(cacheKey); ok {
		return response.code, response.body, true, nil
	}
	generated, gain, catalyst, maximum, err := s.prepareEquipmentMaking(ctx, characterIndex, recipeID, count, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.inventory.Consume(ctx, materials); err != nil {
		return 0, nil, true, fmt.Errorf("player: consume equipment making materials: %w", err)
	}
	if catalyst != 0 {
		identity := "equip-making-catalyst:" + ctx.SessionID + ":" + strconv.FormatUint(seq, 10)
		if _, err := s.wallet.SpendCatalystOnce(ctx, identity, catalyst); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend equipment making catalyst: %w", err)
		}
	}
	created, err := s.grantGeneratedBatch(ctx, "equip-making:"+ctx.SessionID+":"+strconv.FormatUint(seq, 10), generated, true)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: persist made equipment: %w", err)
	}
	if gain > 0 {
		if _, err := s.characters.AddEquipmentTalentExperience(ctx, characterIndex, gain, maximum); err != nil {
			return 0, nil, true, err
		}
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

func (s *EquipmentInventory) breakEquipment(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	indices, err := repeatedEquipmentIndices(request, 2, equipmentBatchLimit)
	if err != nil {
		return 0, nil, true, err
	}
	cacheKey := s.smeltingCacheKey(ctx, "break", seq)
	if response, ok := s.cachedEquipmentReply(cacheKey); ok {
		return response.code, response.body, true, nil
	}

	if s.upgrade == nil || s.inventory == nil {

		return 0, nil, true, errors.New("player: equipment break unavailable")
	}
	rewards := make(map[[2]uint64]uint64)
	positions := make([]int, 0, len(indices))
	for _, index := range indices {
		position := s.equipmentPositionLocked(index)
		if position < 0 {

			return 0, nil, true, fmt.Errorf("player: EquipBreak unknown equipment %d", index)
		}
		entry := s.owned.Equipment[position]
		if entry.UseChar != 0 || entry.LockFlag != 0 || !s.upgrade.CanBreak(entry.ID) {

			return 0, nil, true, fmt.Errorf("player: equipment %d cannot be broken", index)
		}
		result, err := s.upgrade.BreakRewards(entry.ID, entry.Level)
		if err != nil {

			return 0, nil, true, err
		}
		if err := addBattleRewards(rewards, result); err != nil {

			return 0, nil, true, err
		}
		positions = append(positions, position)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(positions)))
	next := cloneEquipmentSnapshot(s.owned)
	for _, position := range positions {
		next.Equipment = append(next.Equipment[:position], next.Equipment[position+1:]...)
	}
	if err := s.commitLocked(ctx, next, "break"); err != nil {

		return 0, nil, true, err
	}

	identity := "equip-break:" + ctx.SessionID + ":" + strconv.FormatUint(seq, 10)
	granted, err := s.inventory.GrantOnce(ctx, identity, aggregateBattleRewards(rewards))
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

func (s *EquipmentInventory) upgradeToBreakAuto(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	indices, err := repeatedEquipmentIndices(request, 2, equipmentBatchLimit)
	if err != nil {
		return 0, nil, true, err
	}
	target, _, err := wire.Varint(request, 3)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipUpgradeToBreakAuto invalid target")
	}
	cacheKey := s.smeltingCacheKey(ctx, "upgrade-break-auto", seq)
	if response, ok := s.cachedEquipmentReply(cacheKey); ok {
		return response.code, response.body, true, nil
	}
	result, err := s.runUpgradeToBreak(ctx, indices, target, "equip-upgrade-break:"+ctx.SessionID+":"+strconv.FormatUint(seq, 10))
	if err != nil {
		return 0, nil, true, err
	}
	response := encodeUpgradeBreakResponse(result, 1, 2, 3, 4, 5, 6, 7)
	s.cacheEquipmentReply(cacheKey, 516, response)
	return 516, response, true, nil
}

func (s *EquipmentInventory) makeToBreakAuto(ctx command.Context, request []byte) (int, []byte, bool, error) {
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
	materials, err := DecodeItemRequest(request, 5, "EquipMakingToBreakAuto")
	if err != nil {
		return 0, nil, true, err
	}
	target, _, err := wire.Varint(request, 6)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipMakingToBreakAuto invalid target")
	}
	cacheKey := s.smeltingCacheKey(ctx, "making-break-auto", seq)
	if response, ok := s.cachedEquipmentReply(cacheKey); ok {
		return response.code, response.body, true, nil
	}
	generated, gain, catalyst, maximum, err := s.prepareEquipmentMaking(ctx, characterIndex, recipeID, count, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.inventory.Consume(ctx, materials); err != nil {
		return 0, nil, true, fmt.Errorf("player: consume equipment making materials: %w", err)
	}
	if catalyst != 0 {
		identity := "equip-making-break-catalyst:" + ctx.SessionID + ":" + strconv.FormatUint(seq, 10)
		if _, err := s.wallet.SpendCatalystOnce(ctx, identity, catalyst); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend automatic equipment making catalyst: %w", err)
		}
	}
	created, err := s.grantGeneratedBatch(ctx, "", generated, false)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: persist auto-break made equipment: %w", err)
	}
	indices := make([]uint64, 0, len(created))
	for _, entry := range created {
		indices = append(indices, entry.InvenIndex)
	}
	if gain > 0 {
		if _, err := s.characters.AddEquipmentTalentExperience(ctx, characterIndex, gain, maximum); err != nil {
			return 0, nil, true, err
		}
	}
	result, err := s.runUpgradeToBreak(ctx, indices, target, "equip-making-break-reward:"+ctx.SessionID+":"+strconv.FormatUint(seq, 10))
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

func rewardItemBundle(items []Item) []byte {
	var bundle []byte
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
	}
	return bundle
}

func (s *EquipmentInventory) batchUse(ctx command.Context, request []byte) (int, []byte, bool, error) {
	entries, err := decodeEquipmentBatchUse(request)
	if err != nil {
		return 0, nil, true, err
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipBatchUse character store unavailable")
	}
	requestedCharacters := make(map[uint64]bool, len(entries))
	for _, entry := range entries {
		if requestedCharacters[entry.CharacterIndex] {
			return 0, nil, true, fmt.Errorf("player: EquipBatchUse repeats character %d", entry.CharacterIndex)
		}
		requestedCharacters[entry.CharacterIndex] = true
		if _, found := s.characters.EquipmentCharacter(ctx, entry.CharacterIndex); !found {
			return 0, nil, true, fmt.Errorf("player: EquipBatchUse unknown character %d", entry.CharacterIndex)
		}
	}

	if len(s.slots) == 0 {

		return 0, nil, true, errors.New("player: EquipBatchUse slot design unavailable")
	}
	desired := make(map[uint64]uint64)
	for _, entry := range entries {
		for slot, index := range entry.Equipment {
			if index == 0 {
				continue
			}
			if desired[index] != 0 {

				return 0, nil, true, fmt.Errorf("player: EquipBatchUse repeats equipment %d", index)
			}
			position := s.equipmentPositionLocked(index)
			if position < 0 {

				return 0, nil, true, fmt.Errorf("player: EquipBatchUse unknown equipment %d", index)
			}
			item := s.owned.Equipment[position]
			if designedSlot, ok := s.slots[item.ID]; !ok || designedSlot != uint64(slot) {

				return 0, nil, true, fmt.Errorf("player: EquipBatchUse equipment %d does not belong in slot %d", index, slot)
			}
			if item.UseChar != 0 && item.UseChar != entry.CharacterIndex && !requestedCharacters[item.UseChar] {

				return 0, nil, true, fmt.Errorf("player: EquipBatchUse equipment %d is still used by character %d", index, item.UseChar)
			}
			desired[index] = entry.CharacterIndex
		}
	}

	next := cloneEquipmentSnapshot(s.owned)
	affected := make(map[uint64]bool, len(entries))
	for character := range requestedCharacters {
		affected[character] = true
	}
	for i := range next.Equipment {
		item := &next.Equipment[i]
		if requestedCharacters[item.UseChar] {
			item.UseChar = 0
		}
	}
	for i := range next.Equipment {
		item := &next.Equipment[i]
		character := desired[item.InvenIndex]
		if character == 0 {
			continue
		}
		if item.UseChar != 0 && item.UseChar != character {
			affected[item.UseChar] = true
		}
		item.UseChar = character
	}
	if err := s.commitLocked(ctx, next, "batch use"); err != nil {

		return 0, nil, true, err
	}

	characterIndices := make([]uint64, 0, len(affected))
	for index := range affected {
		characterIndices = append(characterIndices, index)
	}
	slices.Sort(characterIndices)
	var response []byte
	for _, index := range characterIndices {
		if character, found := s.characters.EquipmentCharacter(ctx, index); found {
			response = wire.AppendBytes(response, 1, character.Response)
		}
	}
	return 276, response, true, nil
}

func decodeEquipmentBatchUse(request []byte) ([]equipmentBatchUse, error) {
	var result []equipmentBatchUse
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: EquipBatchUse invalid batch entry")
		}
		entry := equipmentBatchUse{}
		var characterSeen bool
		if err := wire.Walk(field.Value, func(nested wire.Field) error {
			switch nested.Number {
			case 1:
				if characterSeen || nested.Type != 0 {
					return errors.New("player: EquipBatchUse invalid character")
				}
				entry.CharacterIndex, _ = binary.Uvarint(nested.Value)
				characterSeen = true
			case 2:
				values, err := decodeRepeatedUint64(nested)
				if err != nil {
					return errors.New("player: EquipBatchUse invalid equipment list")
				}
				entry.Equipment = append(entry.Equipment, values...)
			}
			return nil
		}); err != nil {
			return err
		}
		if !characterSeen || entry.CharacterIndex == 0 || len(entry.Equipment) != equipmentSlotCount {
			return errors.New("player: EquipBatchUse requires one character and five equipment slots")
		}
		result = append(result, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("player: EquipBatchUse has no batch entries")
	}
	return result, nil
}

func decodeRepeatedUint64(field wire.Field) ([]uint64, error) {
	switch field.Type {
	case 0:
		value, count := binary.Uvarint(field.Value)
		if count <= 0 {
			return nil, wire.ErrMalformed
		}
		return []uint64{value}, nil
	case 2:
		var result []uint64
		for offset := 0; offset < len(field.Value); {
			value, count := binary.Uvarint(field.Value[offset:])
			if count <= 0 {
				return nil, wire.ErrMalformed
			}
			result = append(result, value)
			offset += count
		}
		return result, nil
	default:
		return nil, wire.ErrMalformed
	}
}

func (s *EquipmentInventory) presetInfo() (int, []byte, bool, error) {

	presets := make([]equipmentPreset, 0, len(s.presets))
	for _, preset := range s.presets {
		presets = append(presets, cloneEquipmentPreset(preset))
	}

	sort.Slice(presets, func(i, j int) bool {
		if presets[i].CharacterIndex != presets[j].CharacterIndex {
			return presets[i].CharacterIndex < presets[j].CharacterIndex
		}
		return presets[i].Slot < presets[j].Slot
	})
	var response []byte
	for i := 0; i < len(presets); {
		character := presets[i].CharacterIndex
		var encoded []byte
		encoded = wire.AppendVarint(encoded, 1, character)
		for i < len(presets) && presets[i].CharacterIndex == character {
			encoded = wire.AppendBytes(encoded, 2, equipmentPresetWire(presets[i]))
			i++
		}
		response = wire.AppendBytes(response, 1, encoded)
	}
	return 253, response, true, nil
}

func equipmentPresetWire(preset equipmentPreset) []byte {
	var out []byte
	if preset.Name != "" {
		out = wire.AppendString(out, 1, preset.Name)
	}
	out = wire.AppendVarint(out, 2, preset.Slot)
	if preset.ResourceID != 0 {
		out = wire.AppendVarint(out, 3, preset.ResourceID)
	}
	if preset.ResourceColor != 0 {
		out = wire.AppendVarint(out, 4, preset.ResourceColor)
	}
	for _, item := range preset.Items {
		var encoded []byte
		if item.Type != 0 {
			encoded = wire.AppendVarint(encoded, 1, item.Type)
		}
		if item.EquipmentIndex != 0 {
			encoded = wire.AppendVarint(encoded, 2, item.EquipmentIndex)
		}
		out = wire.AppendBytes(out, 5, encoded)
	}
	return out
}

func (s *EquipmentInventory) presetSave(ctx command.Context, request []byte) (int, []byte, bool, error) {
	preset, err := decodeEquipmentPresetRequest(request, true)
	if err != nil {
		return 0, nil, true, err
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipPresetSave character store unavailable")
	}
	if _, found := s.characters.EquipmentCharacter(ctx, preset.CharacterIndex); !found {
		return 0, nil, true, fmt.Errorf("player: EquipPresetSave unknown character %d", preset.CharacterIndex)
	}

	if len(s.slots) == 0 {
		return 0, nil, true, errors.New("player: EquipPresetSave slot design unavailable")
	}
	for _, item := range preset.Items {
		if item.EquipmentIndex == 0 {
			continue
		}
		position := s.equipmentPositionLocked(item.EquipmentIndex)
		if position < 0 {
			return 0, nil, true, fmt.Errorf("player: EquipPresetSave unknown equipment %d", item.EquipmentIndex)
		}
		equipment := s.owned.Equipment[position]
		if s.slots[equipment.ID] != item.Type {
			return 0, nil, true, fmt.Errorf("player: EquipPresetSave equipment %d does not match character slot", item.EquipmentIndex)
		}
	}
	if err := s.persistEquipmentPreset(ctx, preset, "save"); err != nil {
		return 0, nil, true, err
	}
	s.presets[equipmentPresetKey{CharacterIndex: preset.CharacterIndex, Slot: preset.Slot}] = cloneEquipmentPreset(preset)
	return 254, nil, true, nil
}

func (s *EquipmentInventory) presetNameChange(ctx command.Context, request []byte) (int, []byte, bool, error) {
	metadata, err := decodeEquipmentPresetRequest(request, false)
	if err != nil {
		return 0, nil, true, err
	}
	key := equipmentPresetKey{CharacterIndex: metadata.CharacterIndex, Slot: metadata.Slot}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipPresetNameChange character store unavailable")
	}
	if _, found := s.characters.EquipmentCharacter(ctx, metadata.CharacterIndex); !found {
		return 0, nil, true, fmt.Errorf("player: EquipPresetNameChange unknown character %d", metadata.CharacterIndex)
	}

	preset, found := s.presets[key]
	if !found {
		preset = metadata
	}
	preset.Name = metadata.Name
	preset.ResourceID = metadata.ResourceID
	preset.ResourceColor = metadata.ResourceColor
	if err := s.persistEquipmentPreset(ctx, preset, "name change"); err != nil {
		return 0, nil, true, err
	}
	s.presets[key] = cloneEquipmentPreset(preset)
	return 259, nil, true, nil
}

func decodeEquipmentPresetRequest(request []byte, withItems bool) (equipmentPreset, error) {
	character, found, err := wire.Varint(request, 2)
	if err != nil || !found || character == 0 {
		return equipmentPreset{}, errors.New("player: equipment preset request missing character")
	}
	slot, found, err := wire.Varint(request, 3)
	if err != nil || !found || slot < 1 || slot > 5 {
		return equipmentPreset{}, errors.New("player: equipment preset request invalid slot")
	}
	nameBytes, _, err := wire.Bytes(request, 4)
	if err != nil || !utf8.Valid(nameBytes) {
		return equipmentPreset{}, errors.New("player: equipment preset request invalid name")
	}
	resourceID, _, err := wire.Varint(request, 5)
	if err != nil || resourceID < 1 || resourceID > 21 {
		return equipmentPreset{}, errors.New("player: equipment preset request invalid resource")
	}
	color, _, err := wire.Varint(request, 6)
	if err != nil || color > 5 {
		return equipmentPreset{}, errors.New("player: equipment preset request invalid color")
	}
	preset := equipmentPreset{CharacterIndex: character, Slot: slot, Name: string(nameBytes), ResourceID: resourceID, ResourceColor: color}
	if withItems {
		err = wire.Walk(request, func(field wire.Field) error {
			if field.Number != 7 {
				return nil
			}
			if field.Type != 2 {
				return errors.New("player: EquipPresetSave invalid equipment entry")
			}
			typeID, _, err := wire.Varint(field.Value, 1)
			if err != nil {
				return err
			}
			index, _, err := wire.Varint(field.Value, 2)
			if err != nil {
				return err
			}
			preset.Items = append(preset.Items, equipmentPresetItem{Type: typeID, EquipmentIndex: index})
			return nil
		})
		if err != nil {
			return equipmentPreset{}, err
		}
	} else {
		preset.Items = make([]equipmentPresetItem, equipmentSlotCount)
		for i := range preset.Items {
			preset.Items[i].Type = uint64(i)
		}
	}
	if err := validateEquipmentPresetShape(preset); err != nil {
		return equipmentPreset{}, fmt.Errorf("player: equipment preset request: %w", err)
	}
	sort.Slice(preset.Items, func(i, j int) bool { return preset.Items[i].Type < preset.Items[j].Type })
	return preset, nil
}

func (s *EquipmentInventory) optionRerollRequest(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipOptionReRoll missing equipment")
	}
	mainLocks, err := repeatedBoolField(request, 3, "EquipOptionReRoll main lock")
	if err != nil {
		return 0, nil, true, err
	}
	subLocks, err := repeatedBoolField(request, 4, "EquipOptionReRoll sub lock")
	if err != nil {
		return 0, nil, true, err
	}
	materials, err := DecodeItemRequest(request, 5, "EquipOptionReRoll")
	if err != nil {
		return 0, nil, true, err
	}
	rerollType, _, err := wire.Varint(request, 6)
	if err != nil || rerollType > 1 {
		return 0, nil, true, errors.New("player: EquipOptionReRoll unsupported reroll type")
	}

	cacheKey := s.smeltingCacheKey(ctx, "option-reroll", seq)
	if reply, ok := s.smeltCache[cacheKey]; ok {
		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	if s.optionReroll == nil || s.wallet == nil || s.inventory == nil {
		return 0, nil, true, errors.New("player: equipment option reroll unavailable")
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipOptionReRoll unknown equipment %d", index)
	}
	current := s.owned.Equipment[position]
	base := current
	if s.pendingReroll != nil {
		if s.pendingReroll.Equipment.InvenIndex != index {
			return 0, nil, true, errors.New("player: another equipment option reroll is awaiting confirmation")
		}
		// A retry from the result screen carries only lock masks. The values
		// being locked are therefore the last candidate, not the still-official
		// equipment options. Keep chaining candidates until confirm/keep clears
		// the pending result.
		base = s.pendingReroll.Equipment
	}
	definition, ok := s.optionReroll.Lookup(current.ID)
	if !ok {
		return 0, nil, true, fmt.Errorf("player: equipment %d has no option reroll design", current.ID)
	}
	if len(mainLocks) != len(base.MainOption) || len(mainLocks) != len(definition.MainGroups) ||
		len(subLocks) != len(base.SubOption) || len(subLocks) != len(definition.SubGroups) {
		return 0, nil, true, errors.New("player: EquipOptionReRoll lock arrays do not match equipment options")
	}
	effectiveMainLocks := append([]bool(nil), mainLocks...)
	if rerollType == 1 && len(effectiveMainLocks) != 0 {
		// The client's has-another-option mode asks the server to leave the
		// first main option alone. It is not a paid lock and remains false in
		// the request mask.
		effectiveMainLocks[0] = true
	}
	lockedCount, unlockedRerollable := uint64(0), uint64(0)
	for i, locked := range mainLocks {
		if base.MainOption[i].GroupID != definition.MainGroups[i] || base.MainOption[i].ID == 0 {
			return 0, nil, true, fmt.Errorf("player: equipment %d main option %d does not match GameData", index, i)
		}
		canReroll := len(s.optionReroll.Groups[definition.MainGroups[i]].Choices) >= 2
		if locked && (i == 0 || !canReroll) {
			return 0, nil, true, fmt.Errorf("player: EquipOptionReRoll main option %d cannot be locked", i)
		}
		if locked {
			lockedCount++
		} else if canReroll && !(rerollType == 1 && i == 0) { //nolint:staticcheck // QF1001
			unlockedRerollable++
		}
	}
	for i, locked := range subLocks {
		if base.SubOption[i].GroupID != definition.SubGroups[i] || base.SubOption[i].ID == 0 {
			return 0, nil, true, fmt.Errorf("player: equipment %d sub option %d does not match GameData", index, i)
		}
		canReroll := len(s.optionReroll.Groups[definition.SubGroups[i]].Choices) >= 2
		if locked && !canReroll {
			return 0, nil, true, fmt.Errorf("player: EquipOptionReRoll sub option %d cannot be locked", i)
		}
		if locked {
			lockedCount++
		} else if canReroll {
			unlockedRerollable++
		}
	}
	if unlockedRerollable == 0 {
		return 0, nil, true, errors.New("player: EquipOptionReRoll must leave a rerollable option unlocked")
	}
	costs, err := s.optionReroll.Cost(current.ID, lockedCount)
	if err != nil {
		return 0, nil, true, err
	}
	gold, consumed, err := validateOptionRerollMaterials(costs, materials, s.optionReroll.Conversion)
	if err != nil {
		return 0, nil, true, err
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for equipment option reroll")
	}
	if len(consumed) != 0 {
		if err := s.inventory.CanConsume(ctx, consumed); err != nil {
			return 0, nil, true, err
		}
	}
	privateLocks := make([]bool, len(definition.PrivateGroups))
	for i := range privateLocks {
		privateLocks[i] = true
	}
	rolled, err := s.optionReroll.RollUnlocked(current.ID, gamedata.EquipmentOptionRerollLocks{Main: effectiveMainLocks, Sub: subLocks, Private: privateLocks})
	if err != nil {
		return 0, nil, true, err
	}
	candidate := cloneEquipment(base)
	for i := range candidate.MainOption {
		if !effectiveMainLocks[i] {
			candidate.MainOption[i] = EquipmentOption{GroupID: rolled.Main[i].GroupID, ID: rolled.Main[i].ID}
		}
	}
	for i := range candidate.SubOption {
		if !subLocks[i] {
			candidate.SubOption[i] = EquipmentOption{GroupID: rolled.Sub[i].GroupID, ID: rolled.Sub[i].ID}
		}
	}
	pending := &equipmentOptionRerollPending{Equipment: candidate}
	if err := s.commitOptionRerollLocked(ctx, pending, consumed, gold, "equip-option-reroll:"+cacheKey); err != nil {
		return 0, nil, true, err
	}
	var response []byte
	for _, option := range candidate.MainOption {
		response = wire.AppendBytes(response, 1, equipmentOptionWire(option))
	}
	for _, option := range candidate.SubOption {
		response = wire.AppendBytes(response, 2, equipmentOptionWire(option))
	}
	s.smeltCache[cacheKey] = smeltingReply{code: 192, body: append([]byte(nil), response...)}
	return 192, response, true, nil
}

func (s *EquipmentInventory) optionRerollConfirm(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipOptionReRollConfirm missing equipment")
	}
	confirm, err := optionalBoolField(request, 3, "EquipOptionReRollConfirm confirm")
	if err != nil {
		return 0, nil, true, err
	}

	cacheKey := s.smeltingCacheKey(ctx, "option-reroll-confirm", seq)
	if reply, ok := s.smeltCache[cacheKey]; ok {

		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	if s.pendingReroll == nil || s.pendingReroll.Equipment.InvenIndex != index {

		return 0, nil, true, fmt.Errorf("player: equipment %d has no option reroll awaiting confirmation", index)
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {

		return 0, nil, true, fmt.Errorf("player: EquipOptionReRollConfirm unknown equipment %d", index)
	}
	next := cloneEquipmentSnapshot(s.owned)
	if confirm {
		next.Equipment[position].MainOption = append([]EquipmentOption(nil), s.pendingReroll.Equipment.MainOption...)
		next.Equipment[position].SubOption = append([]EquipmentOption(nil), s.pendingReroll.Equipment.SubOption...)
	}
	if err := s.persistEquipmentState(ctx, next, nil, true, "option reroll confirm"); err != nil {

		return 0, nil, true, err
	}
	s.owned = next
	s.persisted = cloneEquipmentSnapshot(next)
	s.pendingReroll = nil
	entry := cloneEquipment(next.Equipment[position])

	response := wire.AppendBytes(nil, 1, EquipmentWire(entry))
	if character, ok := s.equippedCharacter(ctx, entry); ok {
		response = wire.AppendBytes(response, 2, character.Response)
	}

	s.smeltCache[cacheKey] = smeltingReply{code: 193, body: append([]byte(nil), response...)}

	return 193, response, true, nil
}

func (s *EquipmentInventory) mainOptionChange(ctx command.Context, request []byte) (int, []byte, bool, error) {
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipMainOptChange missing equipment")
	}
	groupID, groupFound, err := wire.Varint(request, 3)
	if err != nil || !groupFound || groupID == 0 {
		return 0, nil, true, errors.New("player: EquipMainOptChange missing option group")
	}
	optionID, optionFound, err := wire.Varint(request, 4)
	if err != nil || !optionFound || optionID == 0 {
		return 0, nil, true, errors.New("player: EquipMainOptChange missing option")
	}

	if s.optionReroll == nil {
		return 0, nil, true, errors.New("player: equipment option design unavailable")
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipMainOptChange unknown equipment %d", index)
	}
	current := s.owned.Equipment[position]
	definition, ok := s.optionReroll.Lookup(current.ID)
	if !ok || definition.PrivateUniqueCharID == 0 || len(definition.MainGroups) == 0 || len(current.MainOption) == 0 {
		return 0, nil, true, fmt.Errorf("player: equipment %d has no changeable main option", current.ID)
	}
	group, ok := s.optionReroll.Groups[groupID]
	if !ok || groupID != definition.MainGroups[0] || current.MainOption[0].GroupID != groupID || len(group.Choices) < 2 || !optionChoiceExists(group, optionID) {
		return 0, nil, true, fmt.Errorf("player: equipment %d main option %d/%d is not allowed", current.ID, groupID, optionID)
	}

	next := cloneEquipmentSnapshot(s.owned)
	next.Equipment[position].MainOption[0] = EquipmentOption{GroupID: groupID, ID: optionID}
	pending := s.pendingReroll
	pendingDirty := false
	if pending != nil && pending.Equipment.InvenIndex == index {
		copy := &equipmentOptionRerollPending{Equipment: cloneEquipment(pending.Equipment)}
		if len(copy.Equipment.MainOption) == 0 || copy.Equipment.MainOption[0].GroupID != groupID {
			return 0, nil, true, errors.New("player: option reroll candidate has no matching main option")
		}
		copy.Equipment.MainOption[0] = EquipmentOption{GroupID: groupID, ID: optionID}
		pending = copy
		pendingDirty = true
	}
	if err := s.persistEquipmentState(ctx, next, pending, pendingDirty, "main option change"); err != nil {
		return 0, nil, true, err
	}
	s.owned = next
	s.persisted = cloneEquipmentSnapshot(next)
	if pendingDirty {
		s.pendingReroll = pending
	}
	// EquipmentCharacter health reads account equipment through pictorial ownership.
	// Release the equipment lock before resolving the equipped character.

	var response []byte
	if character, ok := s.equippedCharacter(ctx, current); ok {
		response = wire.AppendBytes(response, 1, character.Response)
	}
	return 537, response, true, nil
}

func repeatedBoolField(data []byte, number int, name string) ([]bool, error) {
	var result []bool
	err := wire.Walk(data, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		switch field.Type {
		case 0:
			value, count := binary.Uvarint(field.Value)
			if count <= 0 || value > 1 {
				return fmt.Errorf("player: %s is invalid", name)
			}
			result = append(result, value == 1)
		case 2:
			for offset := 0; offset < len(field.Value); {
				value, count := binary.Uvarint(field.Value[offset:])
				if count <= 0 || value > 1 {
					return fmt.Errorf("player: %s is invalid", name)
				}
				result = append(result, value == 1)
				offset += count
			}
		default:
			return fmt.Errorf("player: %s is invalid", name)
		}
		return nil
	})
	return result, err
}

func optionalBoolField(data []byte, number int, name string) (bool, error) {
	value := false
	seen := false
	err := wire.Walk(data, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		if seen || field.Type != 0 {
			return fmt.Errorf("player: %s is invalid", name)
		}
		raw, count := binary.Uvarint(field.Value)
		if count <= 0 || raw > 1 {
			return fmt.Errorf("player: %s is invalid", name)
		}
		seen = true
		value = raw == 1
		return nil
	})
	return value, err
}

func (s *EquipmentInventory) upgradeOnce(ctx command.Context, request []byte) (int, []byte, bool, error) {
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipUpgrade missing equipment")
	}
	var materials []Item
	err = wire.Walk(request, func(field wire.Field) error {
		if field.Number != 3 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: EquipUpgrade invalid material")
		}
		var item Item
		if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 8: &item.ExpiryTime, 9: &item.SortID, 10: &item.UseCount}); err != nil {
			return err
		}
		if item.Type == 0 || item.Count == 0 || (item.Type == 4 && (item.ID != 0 || item.InvenIndex != 0)) || (item.Type != 4 && (item.ID == 0 || item.InvenIndex == 0)) {
			return errors.New("player: EquipUpgrade invalid material")
		}
		materials = append(materials, item)
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	if len(materials) == 0 {
		return 0, nil, true, errors.New("player: EquipUpgrade has no material")
	}

	entry, success, _, _, err := s.attemptUpgradeLocked(ctx, index, materials)

	if err != nil {
		return 0, nil, true, err
	}
	result := uint64(equipUpgradeFail)
	if success {
		result = equipUpgradeSuccess
	}
	response := wire.AppendBytes(nil, 1, EquipmentWire(entry))
	if result != 0 {
		response = wire.AppendVarint(response, 2, result)
	}
	if character, ok := s.equippedCharacter(ctx, entry); ok {
		response = wire.AppendBytes(response, 3, character.Response)
	}
	return 37, response, true, nil
}

func (s *EquipmentInventory) upgradeSequence(ctx command.Context, request []byte) (int, []byte, bool, error) {
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipSequenceUpgrade missing equipment")
	}
	count, found, err := wire.Varint(request, 3)
	if err != nil || !found || count == 0 || count > 100000 {
		return 0, nil, true, errors.New("player: EquipSequenceUpgrade invalid attempt count")
	}
	goldLimit, _, err := wire.Varint(request, 5)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipSequenceUpgrade invalid gold limit")
	}
	target, _, err := wire.Varint(request, 6)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipSequenceUpgrade invalid target")
	}

	if s.upgrade == nil || s.wallet == nil {

		return 0, nil, true, errors.New("player: equipment upgrade unavailable")
	}
	position := s.equipmentPositionLocked(index)
	if position < 0 {

		return 0, nil, true, fmt.Errorf("player: EquipSequenceUpgrade unknown equipment %d", index)
	}
	maximum := s.upgrade.MaxLevel[s.owned.Equipment[position].ID]
	if target == 0 || target > maximum {
		target = maximum
	}
	var attempts, usedGold uint64
	result := uint64(equipUpgradeStopMaxTryCount)
	var consumed []Item
	var lack []Item
	for attempts < count {
		entry := s.owned.Equipment[position]
		if entry.Level >= maximum {
			result = equipUpgradeStopMaxLevel
			break
		}
		if entry.Level >= target {
			result = equipUpgradeStopTargetLevel
			break
		}
		level, _, designErr := s.upgrade.Level(entry.ID, entry.Level)
		if designErr != nil {

			return 0, nil, true, designErr
		}
		materials, gold, costErr := s.selectUpgradeCosts(ctx, level.Costs)
		if costErr != nil {
			result = equipUpgradeStopNotEnough
			lack = upgradeLackItems(level.Costs)
			break
		}
		if goldLimit != 0 && usedGold+gold > goldLimit {
			result = equipUpgradeStopGoldLimit
			break
		}
		if gold != 0 && !s.wallet.CanSpendGold(gold) {
			result = equipUpgradeStopNotEnough
			lack = upgradeLackItems(level.Costs)
			break
		}
		updated, success, spent, actual, attemptErr := s.attemptUpgradeLocked(ctx, index, materials)
		if attemptErr != nil {

			return 0, nil, true, attemptErr
		}
		attempts++
		usedGold += spent
		consumed = append(consumed, actual...)
		position = s.equipmentPositionLocked(index)
		if success && updated.Level >= target {
			result = equipUpgradeStopTargetLevel
			if updated.Level >= maximum {
				result = equipUpgradeStopMaxLevel
			}
			break
		}
	}
	entry := s.owned.Equipment[position]

	response := wire.AppendBytes(nil, 1, EquipmentWire(entry))
	if character, ok := s.equippedCharacter(ctx, entry); ok {
		response = wire.AppendBytes(response, 2, character.Response)
	}
	response = wire.AppendVarint(response, 3, result)
	response = wire.AppendVarint(response, 4, attempts)
	for _, item := range consumed {
		response = wire.AppendBytes(response, 5, ItemWire(item))
	}
	for _, item := range lack {
		response = wire.AppendBytes(response, 6, ItemWire(item))
	}
	if usedGold != 0 {
		response = wire.AppendVarint(response, 7, usedGold)
	}
	return 176, response, true, nil
}

func (s *EquipmentInventory) smeltOnce(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipSmelting missing equipment")
	}
	materials, err := DecodeItemRequest(request, 3, "EquipSmelting")
	if err != nil {
		return 0, nil, true, err
	}

	cacheKey := s.smeltingCacheKey(ctx, "single", seq)
	if reply, ok := s.smeltCache[cacheKey]; ok {
		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	position, current, err := s.smeltingEquipmentLocked(index)
	if err != nil {
		return 0, nil, true, err
	}
	costs, err := s.smelting.Cost(current.ID)
	if err != nil {
		return 0, nil, true, err
	}
	gold, _, err := validateSmeltingMaterials(costs, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for equipment smelting")
	}
	var itemMaterials []Item
	var consumedMileageMaterial uint64
	for _, item := range materials {
		if item.Type != 4 {
			itemMaterials = append(itemMaterials, item)
		}
		if item.Type == s.smelting.Mileage.UseType && item.ID == s.smelting.Mileage.UseID {
			consumedMileageMaterial += item.Count
		}
	}
	if len(itemMaterials) != 0 {
		if err := s.inventory.CanConsume(ctx, itemMaterials); err != nil {
			return 0, nil, true, err
		}
	}
	candidate, err := s.smelting.RollCandidate(current.ID)
	if err != nil {
		return 0, nil, true, err
	}
	currentScore, err := s.smelting.Score(current.ID, current.Rank)
	if err != nil {
		return 0, nil, true, err
	}
	candidateScore, err := s.smelting.Score(current.ID, candidate)
	if err != nil {
		return 0, nil, true, err
	}
	success := candidateScore > currentScore
	next := cloneEquipmentSnapshot(s.owned)
	if success {
		next.Equipment[position].Rank = append([]uint64(nil), candidate...)
	}
	currency, earned, err := s.commitSmeltingLocked(ctx, next, itemMaterials, gold, consumedMileageMaterial,
		"equip-smelting:"+cacheKey, "smelting")
	if err != nil {
		return 0, nil, true, err
	}
	current = next.Equipment[position]
	response := wire.AppendBytes(nil, 1, EquipmentWire(current))

	if character, ok := s.equippedCharacter(ctx, current); ok {
		response = wire.AppendBytes(response, 2, character.Response)
	}
	if !success {
		response = wire.AppendVarint(response, 3, equipUpgradeFail)
		for _, rank := range candidate {
			response = wire.AppendVarint(response, 4, rank)
		}
	}
	response = appendSmeltingMileage(response, 5, 6, currency.EquipMileageExchangeGage, s.smelting.Mileage, earned)

	s.smeltCache[cacheKey] = smeltingReply{code: 105, body: append([]byte(nil), response...)}

	return 105, response, true, nil
}

func (s *EquipmentInventory) smeltSequence(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, _, _ := wire.Varint(request, 1)
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: EquipSequenceSmelting missing equipment")
	}
	count, found, err := wire.Varint(request, 3)
	if err != nil || !found || count == 0 {
		return 0, nil, true, errors.New("player: EquipSequenceSmelting invalid attempt count")
	}
	target, _, err := wire.Varint(request, 4)
	if err != nil {
		return 0, nil, true, errors.New("player: EquipSequenceSmelting invalid target score")
	}

	cacheKey := s.smeltingCacheKey(ctx, "sequence", seq)
	if reply, ok := s.smeltCache[cacheKey]; ok {
		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	position, current, err := s.smeltingEquipmentLocked(index)
	if err != nil {
		return 0, nil, true, err
	}
	if count > s.smelting.MaxStreak {
		return 0, nil, true, fmt.Errorf("player: EquipSequenceSmelting attempt count %d exceeds %d", count, s.smelting.MaxStreak)
	}
	maximumRanks, err := s.smelting.MaximumRanks(current.ID)
	if err != nil {
		return 0, nil, true, err
	}
	maximumScore, err := s.smelting.Score(current.ID, maximumRanks)
	if err != nil {
		return 0, nil, true, err
	}
	if target > maximumScore {
		return 0, nil, true, fmt.Errorf("player: EquipSequenceSmelting target %d exceeds %d", target, maximumScore)
	}
	costs, err := s.smelting.Cost(current.ID)
	if err != nil {
		return 0, nil, true, err
	}
	currentScore, err := s.smelting.Score(current.ID, current.Rank)
	if err != nil {
		return 0, nil, true, err
	}
	// The 2.35.10 client splits a requested sequence into packets of at most
	// 1000 attempts. Exhausting this packet is not the user's global max-try
	// stop: UPGRADE_SUCCESS tells the client to send the next chunk. Terminal
	// stop values are reserved for target/max score and insufficient resources.
	result := uint64(equipUpgradeSuccess)
	var attempts, successes uint64
	if currentScore >= maximumScore {
		result = equipUpgradeStopMaxLevel
	} else if target != 0 && currentScore >= target {
		result = equipUpgradeStopTargetLevel
	}
	for attempts < count && result == equipUpgradeSuccess {
		if _, _, selectErr := s.selectSmeltingCosts(ctx, costs, attempts+1); selectErr != nil {
			result = equipUpgradeStopNotEnough
			break
		}
		candidate, rollErr := s.smelting.RollCandidate(current.ID)
		if rollErr != nil {
			return 0, nil, true, rollErr
		}
		candidateScore, scoreErr := s.smelting.Score(current.ID, candidate)
		if scoreErr != nil {
			return 0, nil, true, scoreErr
		}
		attempts++
		if candidateScore > currentScore {
			current.Rank = append([]uint64(nil), candidate...)
			currentScore = candidateScore
			successes++
		}
		if currentScore >= maximumScore {
			result = equipUpgradeStopMaxLevel
		} else if target != 0 && currentScore >= target {
			result = equipUpgradeStopTargetLevel
		}
	}
	var consumed, lack []Item
	var gold, mileageMaterial, earned uint64
	currency := s.wallet.Snapshot(ctx)
	if attempts != 0 {
		consumed, gold, err = s.selectSmeltingCosts(ctx, costs, attempts)
		if err != nil {
			return 0, nil, true, err
		}
		for _, item := range consumed {
			if item.Type == s.smelting.Mileage.UseType && item.ID == s.smelting.Mileage.UseID {
				mileageMaterial += item.Count
			}
		}
		var itemMaterials []Item
		for _, item := range consumed {
			if item.Type != 4 {
				itemMaterials = append(itemMaterials, item)
			}
		}
		next := cloneEquipmentSnapshot(s.owned)
		next.Equipment[position].Rank = append([]uint64(nil), current.Rank...)
		currency, earned, err = s.commitSmeltingLocked(ctx, next, itemMaterials, gold, mileageMaterial,
			"equip-sequence-smelting:"+cacheKey, "sequence smelting")
		if err != nil {
			return 0, nil, true, err
		}
		current = next.Equipment[position]
	}
	// NotEnough is used only when the next requested attempt could not be
	// funded. Exhausting this packet retains Success so the client can continue
	// a sequence whose total requested count exceeds the 1000-attempt chunk.
	if result == equipUpgradeStopNotEnough {
		lack = upgradeLackItems(costs)
	}
	response := wire.AppendBytes(nil, 1, EquipmentWire(current))

	if character, ok := s.equippedCharacter(ctx, current); ok {
		response = wire.AppendBytes(response, 2, character.Response)
	}
	response = wire.AppendVarint(response, 3, result)
	response = wire.AppendVarint(response, 4, attempts)
	for _, item := range consumed {
		response = wire.AppendBytes(response, 5, ItemWire(item))
	}
	for _, item := range lack {
		response = wire.AppendBytes(response, 6, ItemWire(item))
	}
	response = appendSmeltingMileage(response, 7, 8, currency.EquipMileageExchangeGage, s.smelting.Mileage, earned)
	if successes != 0 {
		response = wire.AppendVarint(response, 9, successes)
	}

	s.smeltCache[cacheKey] = smeltingReply{code: 177, body: append([]byte(nil), response...)}

	return 177, response, true, nil
}

func appendSmeltingMileage(response []byte, gaugeField, rewardField int, gauge uint64, mileage gamedata.EquipmentSmeltingMileage, earned uint64) []byte {
	if gauge != 0 {
		response = wire.AppendVarint(response, gaugeField, gauge)
	}
	if earned != 0 {
		reward := ItemWire(Item{ID: mileage.RewardID, Type: mileage.RewardType, Count: earned})
		bundle := wire.AppendBytes(nil, 1, reward)
		response = wire.AppendBytes(response, rewardField, bundle)
	}
	return response
}

func (s *EquipmentInventory) clear(ctx command.Context, request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipClear missing equipment")
	}
	characterIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipClear missing character")
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipClear character store unavailable")
	}
	if _, found := s.characters.EquipmentCharacter(ctx, characterIndex); !found {
		return 0, nil, true, fmt.Errorf("player: EquipClear unknown character %d", characterIndex)
	}

	next := cloneEquipmentSnapshot(s.owned)
	position := -1
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {

		return 0, nil, true, fmt.Errorf("player: EquipClear unknown equipment %d", equipmentIndex)
	}
	if next.Equipment[position].UseChar != characterIndex {

		return 0, nil, true, fmt.Errorf("player: EquipClear equipment %d is not used by character %d", equipmentIndex, characterIndex)
	}
	next.Equipment[position].UseChar = 0
	if err := s.commitLocked(ctx, next, "clear"); err != nil {

		return 0, nil, true, err
	}

	// Re-query after the equipment mutation so the shared stat calculator
	// returns HP with the cleared item excluded.
	character, found := s.characters.EquipmentCharacter(ctx, characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: EquipClear character %d disappeared", characterIndex)
	}
	return 36, wire.AppendBytes(nil, 1, character.Response), true, nil
}

func (s *EquipmentInventory) lock(ctx command.Context, request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipLock missing equipment")
	}
	// LockFlag is int32, but proto3 omits its zero value. A missing field 3 is
	// therefore the normal unlock request; present values are restricted to 1.
	lockFlag, present, err := wire.Varint(request, 3)
	if err != nil || (present && lockFlag != 1) {
		return 0, nil, true, errors.New("player: EquipLock invalid lock flag")
	}
	if !present {
		lockFlag = 0
	}

	next := cloneEquipmentSnapshot(s.owned)
	position := -1
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipLock unknown equipment %d", equipmentIndex)
	}
	next.Equipment[position].LockFlag = lockFlag
	if err := s.commitLocked(ctx, next, "lock"); err != nil {
		return 0, nil, true, err
	}
	return 38, nil, true, nil
}

func (s *EquipmentInventory) mark(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, fmt.Errorf("player: %s missing equipment", strings.TrimPrefix(path, "/"))
	}
	mark := ""
	packetCode := 397
	if path == "/EquipMarkSet" {
		raw, present, fieldErr := wire.Bytes(request, 3)
		if fieldErr != nil || !present || !validEquipmentMark(raw) {
			return 0, nil, true, errors.New("player: EquipMarkSet invalid mark")
		}
		mark = string(raw)
		packetCode = 396
	}

	next := cloneEquipmentSnapshot(s.owned)
	position := -1
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: %s unknown equipment %d", strings.TrimPrefix(path, "/"), equipmentIndex)
	}
	next.Equipment[position].Mark = mark
	if err := s.commitLocked(ctx, next, strings.TrimPrefix(path, "/")); err != nil {
		return 0, nil, true, err
	}
	return packetCode, nil, true, nil
}

func (s *EquipmentInventory) change(ctx command.Context, request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipChange missing equipment")
	}
	characterIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipChange missing character")
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipChange character store unavailable")
	}
	if _, found := s.characters.EquipmentCharacter(ctx, characterIndex); !found {
		return 0, nil, true, fmt.Errorf("player: EquipChange unknown character %d", characterIndex)
	}

	if len(s.slots) == 0 {

		return 0, nil, true, errors.New("player: EquipChange slot design unavailable")
	}
	next := cloneEquipmentSnapshot(s.owned)
	position := -1
	for i := range next.Equipment {
		if next.Equipment[i].InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {

		return 0, nil, true, fmt.Errorf("player: EquipChange unknown equipment %d", equipmentIndex)
	}
	selected := next.Equipment[position]
	slot, exists := s.slots[selected.ID]
	if !exists {

		return 0, nil, true, fmt.Errorf("player: EquipChange equipment design %d not found", selected.ID)
	}
	if selected.UseChar != 0 && selected.UseChar != characterIndex {

		return 0, nil, true, fmt.Errorf("player: EquipChange equipment %d belongs to another character", equipmentIndex)
	}
	replaced := false
	for i := range next.Equipment {
		current := &next.Equipment[i]
		if current.InvenIndex == equipmentIndex || current.UseChar != characterIndex {
			continue
		}
		currentSlot, known := s.slots[current.ID]
		if !known {

			return 0, nil, true, fmt.Errorf("player: EquipChange equipped design %d not found", current.ID)
		}
		if currentSlot == slot {
			current.UseChar = 0
			replaced = true
		}
	}
	if !replaced {

		return 0, nil, true, fmt.Errorf("player: EquipChange character %d has no equipment in slot %d", characterIndex, slot)
	}
	next.Equipment[position].UseChar = characterIndex
	if err := s.commitLocked(ctx, next, "change"); err != nil {

		return 0, nil, true, err
	}

	// Max HP depends on the now-current equipment set, so build CharInfo only
	// after the atomic equipment save is visible to the shared stat calculator.
	character, found := s.characters.EquipmentCharacter(ctx, characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: EquipChange character %d disappeared", characterIndex)
	}
	// PacketCodeTypeProto orders EquipChange at 45 (EquipInfo is 34).
	return 45, wire.AppendBytes(nil, 1, character.Response), true, nil
}

func (s *EquipmentInventory) use(ctx command.Context, request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipUse missing equipment")
	}
	characterIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipUse missing character")
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipUse character store unavailable")
	}
	// Find computes account buffs and may query EquipmentInventory.All(). Do
	// not hold the equipment mutex while resolving the character's stats.
	character, found := s.characters.EquipmentCharacter(ctx, characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: EquipUse unknown character %d", characterIndex)
	}

	next := equipmentSnapshot{Version: s.owned.Version, NextIndex: s.owned.NextIndex,
		Equipment: append([]Equipment(nil), s.owned.Equipment...), Granted: make(map[string]uint64, len(s.owned.Granted))}
	maps.Copy(next.Granted, s.owned.Granted)
	position := -1
	for i, current := range next.Equipment {
		if current.InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipUse unknown equipment %d", equipmentIndex)
	}
	next.Equipment[position].UseChar = characterIndex
	if err := s.commitLocked(ctx, next, "use"); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist equipment use: %w", err)
	}
	return 35, wire.AppendBytes(nil, 1, character.Response), true, nil
}
