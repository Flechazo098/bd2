package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
)

func (p *playerInstance) recoverSharedBattle(ctx context.Context) (result error) {
	if p.factory.shared == nil {
		return nil
	}
	service, err := p.socialService()
	if err != nil {
		return err
	}
	tx, err := p.repository.BeginCommand(ctx)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			result = errors.Join(result, tx.Rollback())
		}
	}()
	c := command.Context{Identity: command.Identity{AccountID: p.accountID, SessionID: "recovery", RequestID: "shared-battle"}, State: tx, Cancellation: ctx}
	published := false
	c.Emit = func(event command.Event) error {
		if err := outbox.Publish(tx, p.accountID, event); err != nil {
			return err
		}
		published = true
		return nil
	}
	if err := service.AbandonBattle(c); err != nil {
		return err
	}
	err = tx.Commit()
	finished = true
	if err == nil && published && p.factory.wakeOutbox != nil {
		p.factory.wakeOutbox(p.accountID)
	}
	return err
}

func (p *playerInstance) reconcileSocial(ctx context.Context, request player.Command) (player.Reply, error) {
	var reservation social.Reservation
	if err := json.Unmarshal(request.System.Body, &reservation); err != nil {
		return player.Reply{}, err
	}
	if p.factory.shared == nil || reservation.ActorAccount != p.accountID || reservation.ID == "" {
		return player.Reply{}, social.ErrPermission
	}
	var proof *social.CommitProof
	err := p.readLocal(ctx, request.Identity, func(c command.Context) error {
		raw, found, err := c.State.LoadEntry("social", "receipts", reservation.ID)
		if err != nil || !found {
			return err
		}
		value, err := social.CommittedProof(raw)
		if err == nil {
			proof = &value
		}
		return err
	})
	if err != nil {
		return player.Reply{}, err
	}
	_, err = p.factory.shared.guild.RecoverReservation(ctx, reservation.ID, proof)
	return player.Reply{Responses: []player.Response{{}}}, err
}

func (p *playerInstance) readLocal(ctx context.Context, identity command.Identity, read func(command.Context) error) (result error) {
	tx, err := p.repository.BeginCommand(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			p.assembly = nil
			p.sharedSocial = nil
			result = player.Failure{Cause: errors.Join(result, err), RecoveryRequired: true}
		}
	}()
	result = read(command.Context{Identity: identity, Cancellation: ctx, State: tx})
	if tx.Dirty() {
		p.assembly = nil
		p.sharedSocial = nil
		return player.Failure{Cause: errors.Join(result, errors.New("shared preparation mutated player state")), RecoveryRequired: true}
	}
	return result
}

func (p *playerInstance) guildQualification(ctx context.Context, identity command.Identity, path string) error {
	if !social.Mutates(path) || path == "/GuildInfo" {
		return nil
	}
	return p.readLocal(ctx, identity, func(c command.Context) error {
		rule := p.factory.design.guild.Open
		exp, err := p.assembly.missionService.AchievementExperience()
		if err != nil {
			return err
		}
		if p.factory.design.levelDesign.Level(exp) < rule.SquadLevel {
			return social.ErrIneligible
		}
		if rule.TicketID == 0 {
			return nil
		}
		for _, item := range p.assembly.ownedItems.All(c) {
			if item.Type == 19 && item.ID == rule.TicketID && item.Count > 0 {
				return nil
			}
		}
		return social.ErrIneligible
	})
}

func (p *playerInstance) socialService() (*social.Service, error) {
	if p.sharedSocial != nil {
		return p.sharedSocial, nil
	}
	a := p.assembly
	service, err := social.NewService(p.socialServiceConfig())
	if err != nil {
		return nil, err
	}
	p.sharedSocial = service
	a.battleService.AttachEventBattle(service)
	if err := a.login.AttachGuildRaidSlots(func(ctx command.Context) (uint64, error) {
		state, err := social.LoadRaidState(ctx.State)
		if err != nil {
			return 0, err
		}
		if state.PresetSlots == 0 {
			return a.design.presetDesign.BaseCount, nil
		}
		return state.PresetSlots, nil
	}); err != nil {
		return nil, err
	}
	return service, nil
}

func (p *playerInstance) socialServiceConfig() social.ServiceConfig {
	a := p.assembly
	member := func(ctx context.Context, identity command.Identity) (social.PublicMember, error) {
		var out social.PublicMember
		err := p.readLocal(ctx, identity, func(c command.Context) error {
			owner, err := a.ProfileOwner(c)
			if err != nil {
				return err
			}
			profile, err := a.PublicProfile(c, owner)
			if err != nil {
				return err
			}
			out = social.PublicMember{AccountID: identity.AccountID, OwnerIndex: owner, Name: profile.UserID, TitleID: profile.TitleID, PortraitID: profile.PortraitCostumeID, PortraitDesignID: profile.PortraitDesignID, LastLoginMS: time.Now().UnixMilli()}
			return nil
		})
		return out, err
	}
	var seasons []social.Season
	for _, content := range p.factory.options.calendars.RegularService.Contents {
		if content.ID != 6 {
			continue
		}
		seen := map[uint64]bool{}
		for _, s := range []schedule.Season{content.Current, content.Next} {
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			seasons = append(seasons, social.Season{ID: s.ID, BossGroupID: s.ID, RulesVersion: a.options.gameDataVersion, StartsMS: int64(s.StartMilliseconds), EndsMS: int64(s.EndMilliseconds)})
		}
	}
	return social.ServiceConfig{Owner: p.factory.shared.guild, Profile: member,
		BattleBuffs: a.battleService.PictorialBuffs,
		RaidSeasons: seasons,
		RaidState: func(ctx context.Context, identity command.Identity) (social.RaidState, error) {
			var state social.RaidState
			err := p.readLocal(ctx, identity, func(c command.Context) error { var err error; state, err = social.LoadRaidState(c.State); return err })
			return state, err
		},
		Characters: a.worldService.CharacterService(), Collection: a.collection, Equipment: a.ownedEquipment, Presets: a.design.presetDesign,
		BeginBattle: a.battleService.BeginExternal, BattleRounds: a.battleService.RoundCount,
		RewardBundle: func(c command.Context, id string, rewards []gamedata.Reward) ([]byte, error) {
			return a.eventEconomy.Apply(c, id, nil, rewards)
		},
		Qualify: func(ctx context.Context, identity command.Identity, path string) error {
			return p.guildQualification(ctx, identity, path)
		},
		Supporter: func(ctx context.Context, identity command.Identity, char, costume uint64) (social.SupporterSlot, error) {
			profile, err := member(ctx, identity)
			if err != nil {
				return social.SupporterSlot{}, err
			}
			var out social.SupporterSlot
			err = p.readLocal(ctx, identity, func(c command.Context) error {
				details, err := a.SupporterDetails(c, char, costume)
				if err != nil {
					return err
				}
				out, err = social.SupporterSnapshot(profile, details, char, costume, a.options.gameDataVersion)
				return err
			})
			return out, err
		},
		CheckText: func(ctx context.Context, identity command.Identity, scope, text string) error {
			return a.checkProfileText(command.Context{Identity: identity, Cancellation: ctx}, scope, text)
		},
		Spend: func(c command.Context, id string, costs []gamedata.Reward) error {
			_, err := a.eventEconomy.Apply(c, "guild-cost:"+id, costs, nil)
			return err
		},
		Grant: func(c command.Context, id string, rewards []gamedata.Reward) error {
			_, err := a.eventEconomy.Apply(c, "guild-reward:"+id, nil, rewards)
			return err
		},
	}
}

func (p *playerInstance) prepareSocial(ctx context.Context, request player.Command) (*social.Service, []*social.Plan, error) {
	plans := make([]*social.Plan, len(request.Requests))
	if p.factory.shared == nil {
		return nil, plans, nil
	}
	var service *social.Service
	for i, item := range request.Requests {
		if !social.Owns(item.Path) {
			continue
		}
		if service == nil {
			var err error
			service, err = p.socialService()
			if err != nil {
				return nil, nil, err
			}
		}
		plan, _, err := service.Prepare(ctx, request.Identity, item.Path, item.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("%s prepare: %w", item.Path, err)
		}
		plans[i] = plan
	}
	return service, plans, nil
}
