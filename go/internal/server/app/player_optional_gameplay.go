package app

import (
	"bd2server/internal/server/domain/progression/conditions"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/battle/castle"
	"bd2server/internal/server/domain/cafeteria"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/dating"
	"bd2server/internal/server/domain/life"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
)

func (p *playerAssembly) optionalGameplay(ctx command.Context) error {
	var err error
	p.lifeService, err = life.Open(ctx, p.design.life, life.Options{ValidateAvatar: func(ctx command.Context, raw []byte) error {
		if p.avatarService == nil {
			return fmt.Errorf("life: avatar service unavailable")
		}
		return p.avatarService.ValidateAppearance(ctx, raw)
	}, UnlimitedStacks: p.unlimitedStacks})
	if err != nil {
		return fmt.Errorf("open life: %w", err)
	}
	p.eventEconomy.AttachLife(p.lifeService)
	if err := p.eventEconomy.AttachDatingAP(p.design.dating.Default.V(5)); err != nil {
		return err
	}
	p.datingService, err = dating.Open(ctx, p.gameplayStore, p.design.dating, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("open dating: %w", err)
	}
	p.cafeteriaService, err = cafeteria.Open(ctx, p.gameplayStore, p.design.cafeteria, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("open cafeteria: %w", err)
	}
	p.datingService.SetResetSeconds(p.design.eventAPReset.ResetSeconds)
	p.cafeteriaService.SetResetSeconds(p.design.eventAPReset.ResetSeconds)
	p.cafeteriaService.AttachDatingTicket(p.design.contentOpeningDesign.Dating.TicketID, func(ctx command.Context, ticket uint64) bool {
		for _, item := range p.ownedItems.All(ctx) {
			if item.Type == 19 && item.ID == ticket && item.Count > 0 {
				return true
			}
		}
		return false
	})
	p.cafeteriaService.AttachOwnedCostumes(func() []uint64 {
		var ids []uint64
		for _, costume := range p.collection.Costumes() {
			ids = append(ids, costume.ID)
		}
		return ids
	})
	p.cafeteriaService.AttachDatingHasEpisode(p.datingService.HasEpisode)
	p.worldService.AttachFieldPackEconomy(p.eventEconomy)
	p.datingService.AttachFirstMeeting(p.cafeteriaService.CanMeet)
	p.castleService = castle.NewService(p.design.castle)
	p.castleService.AttachTotalWarTextCheck(p.checkProfileText)
	if err := p.castleService.AttachTotalWarRuntime(p.worldService.CharacterService(), p.collection, p.ownedEquipment, p.wallet); err != nil {
		return err
	}
	if err := p.login.AttachTotalWarSlots(p.castleService.TotalWarPresetSlotCount); err != nil {
		return err
	}
	p.castleService.AttachTotalWarCompletion(func(ctx command.Context, identity string, score uint64, costumes []uint64) error {
		owner, err := p.ProfileOwner(ctx)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(deck.PopularityPublication{CompletedAt: uint64(time.Now().UnixMilli()), Score: score, OwnerIndex: owner, TotalWarCostumes: append([]uint64(nil), costumes...)})
		if err != nil {
			return err
		}
		return ctx.Publish(command.Event{ID: "totalwar:" + identity, Topic: deck.PopularityTopic, Payload: payload})
	})
	p.castleService.AttachTotalWarSettled(func(ctx command.Context, total uint64) error {
		return p.recordMissions(ctx, []conditions.Event{{Type: conditions.TotalWarTryCount, Count: 1}, {Type: conditions.TotalWarResultScore, Sub: total, Count: 1}})
	})
	p.castleService.AttachCurrentPack(p.worldService.CurrentPackID)
	p.castleService.AttachEvidence(p.battleService.SettlementEvidence)
	p.castleService.AttachRewards(func(ctx command.Context, identity string, rewards []gamedata.Reward) ([]byte, error) {
		return p.eventEconomy.Apply(ctx, identity, nil, rewards)
	})
	p.castleService.AttachSeasonSettlement(func(id uint64) bool {
		calendar := p.options.calendars.RegularService
		if calendar == nil {
			return false
		}
		now := uint64(time.Now().UnixMilli())
		for _, content := range calendar.Contents {
			if content.ID != 2 {
				continue
			}
			for _, season := range []schedule.Season{content.Current, content.Next} {
				if season.ID == id {
					return !season.Error && now >= season.EndMilliseconds+calendar.CalculateMilliseconds
				}
			}
			return id < content.Current.ID
		}
		return false
	})
	p.castleService.AttachPeriod(func(command.Context) (uint64, bool, error) {
		if p.options.calendars.RegularService == nil {
			return 0, false, fmt.Errorf("castle: missing regular calendar")
		}
		now := uint64(time.Now().UnixMilli())
		for _, content := range p.options.calendars.RegularService.Contents {
			if content.ID != 2 {
				continue
			}
			for _, season := range []schedule.Season{content.Current, content.Next} {
				if now >= season.StartMilliseconds && now < season.EndMilliseconds && !season.Error {
					return season.ID, true, nil
				}
			}
			return content.Current.ID, false, nil
		}
		return 0, false, fmt.Errorf("castle: missing season")
	})
	p.castleService.AttachTotalWarValidation(func(ctx command.Context, participants []castle.Participant) error {
		for _, participant := range participants {
			character, owned := p.worldService.CharacterService().Find(ctx, participant.CharacterIndex)
			if !owned || character.ID != participant.CharacterID {
				return fmt.Errorf("castle: character unavailable: character=%d character_index=%d", participant.CharacterID, participant.CharacterIndex)
			}
			if participant.CostumeIndex == 0 && participant.CostumeID != 0 &&
				participant.CostumeID == p.design.castle.Characters[participant.CharacterID].DefaultCostume {
				continue
			}
			// Field battle costume changes do not update the equipped costume.
			var costume roster.Costume
			if participant.CostumeIndex == 0 {
				costume, owned = p.collection.CostumeByID(participant.CostumeID)
			} else {
				costume, owned = p.collection.CostumeByIndex(participant.CostumeIndex)
			}
			if !owned || costume.ID != participant.CostumeID {
				return fmt.Errorf("castle: costume unavailable: character=%d character_index=%d costume=%d costume_index=%d", participant.CharacterID, participant.CharacterIndex, participant.CostumeID, participant.CostumeIndex)
			}
			definition, found := p.design.rewardCostumes.Character(costume.ID)
			if !found || definition.ID != character.ID && !slices.Contains(definition.GrowthCharacterIDs, character.ID) {
				return fmt.Errorf("castle: costume belongs to a different character: character=%d character_index=%d costume=%d costume_index=%d", character.ID, character.InvenIndex, participant.CostumeID, participant.CostumeIndex)
			}
		}
		return nil
	})
	p.battleService.AttachEventBattle(p.castleService)
	return nil
}
