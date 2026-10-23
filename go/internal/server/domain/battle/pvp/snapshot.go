package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/progression/pictorial"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"slices"
	"sort"
)

type OwnedSnapshotBuilder struct {
	Design     *gamedata.PvpDesign
	Characters *roster.CharacterStore
	Collection *roster.CollectionStore
	Equipment  *assets.EquipmentInventory
	Buffs      func(command.Context) ([]gamedata.PictorialBuffStat, error)
	// CostumeCosts returns the current colosseum price of every ranked
	// costume; unranked costumes cost ColosseumDefaults.CostDefault.
	CostumeCosts func(command.Context) (map[uint64]uint64, error)
	OwnerIndex   uint64
}

func (b *OwnedSnapshotBuilder) Freeze(ctx command.Context, r FreezeRequest) (FullSnapshot, error) {
	if b.Design == nil || b.Characters == nil || b.Collection == nil || b.Equipment == nil || b.Buffs == nil || b.CostumeCosts == nil || b.OwnerIndex == 0 {
		return FullSnapshot{}, fmt.Errorf("pvp: incomplete snapshot builder")
	}
	previousContents := map[uint64][]byte{}
	previousEquipment := map[uint64][]byte{}
	for _, entry := range r.Previous.Contents {
		index, e := scalar(entry, 1)
		if e != nil {
			return FullSnapshot{}, e
		}
		previousContents[index] = entry
	}
	equipment, e := messages(r.Previous.FullDeck, 4)
	if e != nil {
		return FullSnapshot{}, e
	}
	for _, item := range equipment {
		index, e := scalar(item, 1)
		if e != nil {
			return FullSnapshot{}, e
		}
		previousEquipment[index] = item
	}
	requested := map[uint64][]byte{}
	for _, entry := range r.Contents {
		index, e := scalar(entry, 1)
		if e != nil || index == 0 || requested[index] != nil {
			return FullSnapshot{}, fmt.Errorf("pvp: invalid saved equipment character")
		}
		requested[index] = entry
	}
	liveEquipment := map[uint64]assets.Equipment{}
	for _, item := range b.Equipment.All(ctx) {
		liveEquipment[item.InvenIndex] = item
	}
	allCostumes := b.Collection.Costumes()
	costumeByIndex := map[uint64]roster.Costume{}
	for _, item := range allCostumes {
		costumeByIndex[item.InvenIndex] = item
	}
	var banned, costLimit []uint64
	var costs map[uint64]uint64
	if r.Mode == Colosseum {
		banned, e = b.Design.ColosseumRule(r.Season, 30)
		if e != nil {
			return FullSnapshot{}, e
		}
		if costLimit, e = b.Design.ColosseumRule(r.Season, 32); e != nil {
			return FullSnapshot{}, e
		}
		if len(costLimit) != 0 {
			if costs, e = b.CostumeCosts(ctx); e != nil {
				return FullSnapshot{}, e
			}
		}
	}
	var totalCost uint64
	var result FullSnapshot
	var bindings []assets.PresetEquipmentBinding
	usedEquipment := map[uint64]bool{}
	usedCostumes := map[uint64]bool{}
	deckCharacters := map[uint64]bool{}
	for _, entry := range r.Deck {
		index, e := scalar(entry, 1)
		if e != nil {
			return result, e
		}
		c, ok := b.Characters.Find(ctx, index)
		if !ok || int64(c.ExpiryTime) > 0 {
			return result, fmt.Errorf("pvp: formation character is not permanent ownership")
		}
		deckCharacters[index] = true
		selected, e := scalar(entry, 4)
		if e != nil {
			return result, e
		}
		costume, ok := costumeByIndex[selected]
		if !ok || costume.UseChar != index {
			return result, fmt.Errorf("pvp: selected costume is not owned by formation character")
		}
		if slices.Contains(banned, costume.ID) {
			return result, fmt.Errorf("pvp: selected costume banned this season")
		}
		if cost, ranked := costs[costume.ID]; ranked {
			totalCost += cost
		} else {
			totalCost += b.Design.Colosseum.CostDefault
		}
		if e = b.validateSkills(index, entry, costumeByIndex); e != nil {
			return result, e
		}
		contents := requested[index]
		if r.Renew {
			contents = nil
		} else if contents == nil {
			contents = previousContents[index]
		}
		if contents == nil {
			contents = wire.AppendVarint(nil, 1, index)
			contents = wire.AppendVarint(contents, 3, c.ConnectPotentialCostume)
			var equipped []assets.Equipment
			for _, item := range liveEquipment {
				if item.UseChar == index {
					equipped = append(equipped, item)
				}
			}
			sort.Slice(equipped, func(i, j int) bool { return equipped[i].InvenIndex < equipped[j].InvenIndex })
			for _, item := range equipped {
				slot, e := b.equipmentSlot(ctx, index, item)
				if e != nil {
					return result, e
				}
				reference := wire.AppendVarint(nil, 1, item.InvenIndex)
				reference = wire.AppendVarint(reference, 2, slot)
				contents = wire.AppendBytes(contents, 2, reference)
			}
		}
		binding, exists, e := wire.Varint(contents, 3)
		if !exists {
			binding = c.ConnectPotentialCostume
			contents = wire.AppendVarint(contents, 3, binding)
		}
		if e != nil {
			return result, e
		}
		if binding != c.ConnectPotentialCostume {
			oldBinding, e := scalar(previousContents[index], 3)
			if e != nil || previousContents[index] == nil || oldBinding != binding {
				return result, fmt.Errorf("pvp: binding was neither live nor previously frozen")
			}
		}
		c.ConnectPotentialCostume = binding
		c.UseCostume, c.CostumeID = selected, costume.ID
		result.FullDeck = wire.AppendBytes(result.FullDeck, 1, entry)
		result.FullDeck = wire.AppendBytes(result.FullDeck, 2, roster.CharacterWire(c))
		result.Contents = append(result.Contents, append([]byte(nil), contents...))
		references, e := messages(contents, 2)
		if e != nil {
			return result, e
		}
		equipmentBinding := assets.PresetEquipmentBinding{CharacterIndex: index, Equipment: make([]uint64, 5)}
		seenSlots := map[uint64]bool{}
		for _, reference := range references {
			id, e := scalar(reference, 1)
			if e != nil {
				return result, e
			}
			slot, e := scalar(reference, 2)
			if e != nil || slot >= 5 || seenSlots[slot] || id != 0 && usedEquipment[id] {
				return result, fmt.Errorf("pvp: invalid equipment slot or duplicate equipment")
			}
			seenSlots[slot] = true
			if id == 0 {
				continue
			}
			item, owned := liveEquipment[id]
			frozen := previousEquipment[id]
			if !owned {
				return result, fmt.Errorf("pvp: saved equipment no longer owned")
			}
			equipmentBinding.Equipment[slot] = id
			usedEquipment[id] = true
			if r.Renew || frozen == nil {
				item.UseChar = index
				frozen = assets.EquipmentWire(item)
			} else {
				frozen, _, e = wire.ReplaceVarint(frozen, 2, index)
				if e != nil {
					return result, e
				}
			}
			result.FullDeck = wire.AppendBytes(result.FullDeck, 4, frozen)
		}
		bindings = append(bindings, equipmentBinding)
		for _, owned := range allCostumes {
			if owned.UseChar == index && !usedCostumes[owned.InvenIndex] {
				usedCostumes[owned.InvenIndex] = true
				result.FullDeck = wire.AppendBytes(result.FullDeck, 3, roster.CostumeWire(owned))
			}
		}
	}
	if len(costLimit) != 0 && totalCost > costLimit[0] {
		return result, fmt.Errorf("pvp: costume cost %d exceeds season limit %d", totalCost, costLimit[0])
	}
	for index := range requested {
		if !deckCharacters[index] {
			return result, fmt.Errorf("pvp: saved equipment references a character outside formation")
		}
	}
	if len(bindings) > 0 {
		if e = b.Equipment.ValidatePresetEquipment(ctx, bindings); e != nil {
			return result, e
		}
	}
	buffs, e := b.Buffs(ctx)
	if e != nil {
		return result, e
	}
	for _, buff := range buffs {
		result.FullDeck = wire.AppendBytes(result.FullDeck, 5, pictorial.BuffWire(buff))
	}
	awake := b.Collection.CharAwakeStates()
	ids := make([]uint64, 0, len(awake))
	for id := range awake {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		progress := awake[id]
		raw := wire.AppendVarint(nil, 1, id)
		for i, level := range progress.ImprintLevels {
			raw = wire.AppendVarint(raw, i+2, level)
		}
		if progress.IsAwake {
			raw = wire.AppendVarint(raw, 5, 1)
		}
		raw = wire.AppendVarint(raw, 6, b.OwnerIndex)
		result.FullDeck = wire.AppendBytes(result.FullDeck, 6, raw)
	}
	if r.Mode == Colosseum {
		result.FullDeck = appendInts(result.FullDeck, 7, r.AttackBless)
		result.FullDeck = appendInts(result.FullDeck, 8, r.DefenseBless)
	}
	return result, nil
}

func (b *OwnedSnapshotBuilder) equipmentSlot(ctx command.Context, index uint64, item assets.Equipment) (uint64, error) {
	for slot := range uint64(5) {
		binding := assets.PresetEquipmentBinding{CharacterIndex: index, Equipment: make([]uint64, 5)}
		binding.Equipment[slot] = item.InvenIndex
		if b.Equipment.ValidatePresetEquipment(ctx, []assets.PresetEquipmentBinding{binding}) == nil {
			return slot, nil
		}
	}
	return 0, fmt.Errorf("pvp: unknown owned equipment slot")
}
func (b *OwnedSnapshotBuilder) validateSkills(index uint64, entry []byte, costumes map[uint64]roster.Costume) error {
	for _, field := range []int{5, 6} {
		skills, e := messages(entry, field)
		if e != nil {
			return e
		}
		if len(skills) > len(costumes)+1 {
			return fmt.Errorf("pvp: excessive skill sequence")
		}
		seen := map[uint64]bool{}
		for _, skill := range skills {
			costume, e := scalar(skill, 1)
			if e != nil {
				return e
			}
			burst, e := scalar(skill, 2)
			if e != nil {
				return e
			}
			owned, ok := costumes[costume]
			if !ok || owned.UseChar != index || burst > owned.BurstLevel || seen[costume] {
				return fmt.Errorf("pvp: invalid owned costume skill sequence")
			}
			seen[costume] = true
		}
	}
	return nil
}

func (p PublishedSnapshot) Deck() [][]byte {
	entries, _ := messages(p.Snapshot.FullDeck, 1)
	return entries
}
func (s *Service) AttachCompletion(f func(command.Context, Mode, string, bool, [][]byte) ([]byte, error)) {
	s.completion = f
}

func (s *Service) AttachPreparation(f func(command.Context, Mode, string, PublishedSnapshot, []byte) error) {
	s.preparation = f
}
