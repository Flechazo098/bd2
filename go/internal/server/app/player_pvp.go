package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/storage/shared"
	"context"
	"fmt"
	"time"
)

func (p *playerAssembly) multiplayerPVP(ctx command.Context) error {
	if p.pvpSource == nil {
		return nil
	}
	owner, err := p.ProfileOwner(ctx)
	if err != nil {
		return err
	}
	profile, err := p.PublicProfile(ctx, owner)
	if err != nil {
		return err
	}
	exp, err := p.missionService.AchievementExperience()
	if err != nil {
		return err
	}
	public := pvp.Profile{OwnerID: ctx.AccountID, OwnerIndex: owner, Name: profile.UserID, XP: exp, PortraitCostume: profile.PortraitCostumeID, PortraitDesign: profile.PortraitDesignID, Title: profile.TitleID, Guild: profile.GuildBase}
	builder := &pvp.OwnedSnapshotBuilder{Design: p.design.pvp.Design, Characters: p.worldService.CharacterService(), Collection: p.collection, Equipment: p.ownedEquipment, OwnerIndex: owner,
		Buffs: func(ctx command.Context) ([]gamedata.PictorialBuffStat, error) {
			_, buffs, err := p.pictorialService.Snapshot(ctx)
			return buffs, err
		},
		CostumeCosts: p.colosseumCostumeCosts}
	p.pvpService, err = pvp.Open(ctx, p.design.pvp, public, p.pvpSource, builder,
		func(ctx command.Context, id string, cost []gamedata.BattleReward) error {
			_, err := p.eventEconomy.Apply(ctx, id, pvpRewards(cost), nil)
			return err
		}, func(ctx command.Context, id string, reward []gamedata.BattleReward) ([]byte, error) {
			return p.eventEconomy.Apply(ctx, id, nil, pvpRewards(reward))
		})
	if err != nil {
		return err
	}
	if err = p.pvpService.AttachPlayerViews(func(ctx command.Context, typ uint64) (uint64, error) {
		field, ok := events.AdditionalCurrencyFields[typ]
		if !ok {
			return 0, fmt.Errorf("pvp: unsupported ticket currency")
		}
		values, err := p.eventEconomy.AdditionalCurrencies(ctx)
		return values[field], err
	}, func(ctx command.Context) (pvp.Profile, error) {
		current, err := p.PublicProfile(ctx, owner)
		if err != nil {
			return pvp.Profile{}, err
		}
		xp, err := p.missionService.AchievementExperience()
		if err != nil {
			return pvp.Profile{}, err
		}
		return pvp.Profile{OwnerID: ctx.AccountID, OwnerIndex: owner, Name: current.UserID, XP: xp, PortraitCostume: current.PortraitCostumeID, PortraitDesign: current.PortraitDesignID, Title: current.TitleID, Guild: current.GuildBase}, nil
	}); err != nil {
		return err
	}
	if err = p.login.AttachColosseumSlots(p.pvpService); err != nil {
		return err
	}
	p.pvpService.AttachCompletion(p.completePVP)
	p.pvpService.AttachPreparation(p.preparePVPRecommendation)
	return p.login.AttachAdditionalCurrencies(pvpLoginCurrencies{p})
}

type pvpLoginCurrencies struct{ player *playerAssembly }

func (p pvpLoginCurrencies) AdditionalCurrencies(ctx command.Context) (map[int]uint64, error) {
	if err := p.player.pvpService.RefreshTickets(ctx); err != nil {
		return nil, err
	}
	return p.player.eventEconomy.AdditionalCurrencies(ctx)
}

func pvpRewards(in []gamedata.BattleReward) []gamedata.Reward {
	out := make([]gamedata.Reward, len(in))
	for i, reward := range in {
		out[i] = gamedata.Reward(reward)
	}
	return out
}

func (p *playerAssembly) colosseumCostumeCosts(ctx command.Context) (map[uint64]uint64, error) {
	cancel := ctx.Cancellation
	if cancel == nil {
		cancel = context.Background()
	}
	costs := map[uint64]uint64{}
	err := p.shared.store.View(cancel, func(tx *shared.Tx) error {
		table, err := p.shared.colosseumCosts(tx, uint64(time.Now().UnixMilli()))
		for _, row := range table.Costumes {
			costs[row.ID] = row.Cost
		}
		return err
	})
	return costs, err
}
