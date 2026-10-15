package app

import (
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/protocol/ownership"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"math"
)

func (p *playerAssembly) preparePVPRecommendation(ctx command.Context, mode pvp.Mode, identity string, snapshot pvp.PublishedSnapshot, request []byte) error {
	if mode != pvp.Mirror {
		return nil
	}
	var scenario deck.RecommendContext
	found := false
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number != 103 {
			return nil
		}
		if f.Type != 2 || found {
			return fmt.Errorf("pvp: invalid recommendation context")
		}
		found = true
		fields := map[int]*uint64{2: &scenario.BattleMode, 3: &scenario.PackID, 4: &scenario.BattleIndex, 5: &scenario.Level, 6: &scenario.TeamCountType, 7: &scenario.DeckType}
		seen := map[int]bool{}
		return wire.Walk(f.Value, func(field wire.Field) error {
			target := fields[field.Number]
			if target == nil {
				return nil
			}
			if field.Type != 0 || seen[field.Number] {
				return fmt.Errorf("pvp: invalid recommendation context field")
			}
			seen[field.Number] = true
			value, _ := binary.Uvarint(field.Value)
			if value > math.MaxInt32 {
				return fmt.Errorf("pvp: recommendation context outside protocol range")
			}
			*target = value
			return nil
		})
	})
	if err != nil || !found {
		return err
	}
	if scenario.BattleMode != 6 || scenario.Level != 0 || scenario.TeamCountType != 0 || scenario.DeckType != 0 {
		return fmt.Errorf("pvp: invalid offense recommendation context")
	}
	r := deck.RecommendRecord{Context: scenario, Identity: deck.RecommendIdentity{OwnerIndex: snapshot.Profile.OwnerIndex, UserID: snapshot.Profile.Name, UserExp: snapshot.Profile.XP, PortraitCostumeID: snapshot.Profile.PortraitCostume, PortraitDesignID: snapshot.Profile.PortraitDesign, TitleID: snapshot.Profile.Title, GuildBase: append([]byte(nil), snapshot.Profile.Guild...)}, Awake: map[uint64]roster.CharAwakeProgress{}}
	err = wire.Walk(snapshot.Snapshot.FullDeck, func(f wire.Field) error {
		if f.Number < 1 || f.Number > 6 || f.Number == 5 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("pvp: invalid frozen recommendation row")
		}
		switch f.Number {
		case 1:
			var entry deck.DeckEntry
			entry.CharacterInvenIndex, _, err = wire.Varint(f.Value, 1)
			if err != nil {
				return err
			}
			entry.CostumeInvenIndex, _, err = wire.Varint(f.Value, 2)
			if err != nil {
				return err
			}
			entry.Slot, _, err = wire.Varint(f.Value, 3)
			r.Deck = append(r.Deck, entry)
		case 2:
			value, e := ownership.DecodeCharacter(f.Value)
			if e != nil {
				return e
			}
			character := roster.Character{InvenIndex: value.InvenIndex, ID: value.ID, HP: value.HP, Level: value.Level, CostumeID: value.CostumeID, Exp: value.Exp, UseCostume: value.UseCostume, TalentLevel: value.TalentLevel, TalentExp: value.TalentExp, SolidarityReward: value.SolidarityReward, ExpiryTime: value.ExpiryTime, ConnectPotentialCostume: value.ConnectPotentialCostume}
			for _, book := range value.Pictorialbook {
				character.Pictorialbook = append(character.Pictorialbook, roster.Pictorial{ID: book.ID, GroupID: book.GroupID})
			}
			r.Characters = append(r.Characters, character)
		case 3:
			value, e := ownership.DecodeCostume(f.Value)
			if e != nil {
				return e
			}
			costume := roster.Costume{InvenIndex: value.InvenIndex, ID: value.ID, Level: value.Level, UseChar: value.UseChar, SortID: value.SortID, PotentialLockIDs: value.PotentialLockIDs, PotentialIDs: value.PotentialIDs, DesignID: value.DesignID, BurstLevel: value.BurstLevel, TimeValue: value.TimeValue}
			for _, book := range value.Pictorialbook {
				costume.Pictorialbook = append(costume.Pictorialbook, roster.Pictorial{ID: book.ID, GroupID: book.GroupID})
			}
			r.Costumes = append(r.Costumes, deck.RecommendCostume{Costume: costume, Potential: append([]uint64(nil), value.PotentialIDs...), Locked: append([]uint64(nil), value.PotentialLockIDs...)})
		case 4:
			value, e := ownership.DecodeEquipment(f.Value)
			if e != nil {
				return e
			}
			equipment := assets.Equipment{InvenIndex: value.InvenIndex, ID: value.ID, Level: value.Level, UseChar: value.UseChar, KeepFlag: value.KeepFlag, LockFlag: value.LockFlag, SortID: value.SortID, Mark: value.Mark, Rank: value.Rank}
			for _, option := range value.MainOption {
				equipment.MainOption = append(equipment.MainOption, assets.EquipmentOption(option))
			}
			for _, option := range value.SubOption {
				equipment.SubOption = append(equipment.SubOption, assets.EquipmentOption(option))
			}
			if value.PrivateOption != nil {
				option := assets.EquipmentOption(*value.PrivateOption)
				equipment.PrivateOption = &option
			}
			r.Equipment = append(r.Equipment, equipment)
		case 6:
			unique, _, e := wire.Varint(f.Value, 1)
			if e != nil {
				return e
			}
			var awake roster.CharAwakeProgress
			for i := range awake.ImprintLevels {
				awake.ImprintLevels[i], _, e = wire.Varint(f.Value, i+2)
				if e != nil {
					return e
				}
			}
			enabled, _, e := wire.Varint(f.Value, 5)
			if e != nil || enabled > 1 {
				return fmt.Errorf("pvp: invalid frozen awake state")
			}
			awake.IsAwake = enabled == 1
			for _, character := range r.Characters {
				if p.design.costumePotentialDesign.CharacterUnique[character.ID] == unique {
					r.Awake[unique] = awake
					break
				}
			}
		}
		return err
	})
	if err != nil {
		return err
	}
	costumes := make([]roster.Costume, 0, len(r.Costumes))
	for _, costume := range r.Costumes {
		costumes = append(costumes, costume.Costume)
	}
	r.Power, err = p.rosterProgressService.SnapshotBattlePower(r.Characters, costumes, r.Equipment, r.Awake)
	if err != nil {
		return err
	}
	return p.deckStateStore.PrepareRecommendationSnapshot(ctx, "pvp:"+identity, r)
}
