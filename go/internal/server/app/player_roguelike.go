package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/battle/roguelike"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/outbox"

	"context"
	"encoding/json"
	"fmt"

	"time"
)

func (s *sharedRuntime) consumeRogueRanking(ctx context.Context, event outbox.Pending) error {
	var p roguelike.RankPublication
	if e := json.Unmarshal(event.Envelope.Event.Payload, &p); e != nil {
		return e
	}
	if p.Account != event.Envelope.Source {
		return fmt.Errorf("roguelike: ranking source mismatch")
	}
	return s.rogueRanks.Publish(ctx, p)
}
func (p *playerAssembly) rogueLike(ctx command.Context) error {
	owner, e := p.ProfileOwner(ctx)
	if e != nil {
		return e
	}
	service, e := roguelike.Open(p.gameplayStore, p.design.roguelike, roguelike.Options{Access: func(ctx command.Context) error {
		rule := p.design.roguelike.Open
		xp, e := p.missionService.AchievementExperience()
		if e != nil {
			return e
		}
		if p.design.levelDesign.Level(xp) < rule.SquadLevel {
			return fmt.Errorf("roguelike: squad level locked")
		}
		if rule.TicketID > 0 {
			found := false
			for _, i := range p.ownedItems.All(ctx) {
				if i.Type == 19 && i.ID == rule.TicketID && i.Count > 0 {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("roguelike: content locked")
			}
		}
		return nil
	}, Economy: p.eventEconomy, Owner: owner, Evidence: p.battleService.SettlementEvidence,
		CostumeOwned: func(id uint64) bool {
			_, owned := p.collection.CostumeByID(id)
			return owned
		},
		SeasonSettled: func(id uint64) bool {
			calendar := p.options.calendars.RegularService
			if calendar == nil {
				return false
			}
			now := uint64(time.Now().UnixMilli())
			for _, content := range calendar.Contents {
				if content.ID != 5 {
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
		},
		Period: func(command.Context) (roguelike.Period, error) {
			calendar := p.options.calendars.RegularService
			if calendar == nil {
				return roguelike.Period{}, fmt.Errorf("roguelike: calendar unavailable")
			}
			now := uint64(time.Now().UnixMilli())
			var regular uint64
			for _, r := range calendar.Regular {
				if r.ContentID == 5 {
					regular = r.Season
				}
			}
			for _, c := range calendar.Contents {
				if c.ID != 5 {
					continue
				}
				for _, s := range []schedule.Season{c.Current, c.Next} {
					if now >= s.StartMilliseconds && now < s.EndMilliseconds && !s.Error {
						return roguelike.Period{Season: s.ID, Regular: regular, Active: true}, nil
					}
				}
				return roguelike.Period{Season: c.Current.ID, Regular: regular}, nil
			}
			return roguelike.Period{}, fmt.Errorf("roguelike: missing schedule")
		},
		Tickets: func(ctx command.Context, key string, count uint64) error {
			free, e := p.eventEconomy.CurrencyBalance(ctx, 35)
			if e != nil {
				return e
			}
			var costs []gamedata.Reward
			if use := min(free, count); use > 0 {
				costs = append(costs, gamedata.Reward{Type: 35, Count: use})
				count -= use
			}
			if count > 0 {
				costs = append(costs, gamedata.Reward{Type: 38, Count: count})
			}
			_, e = p.eventEconomy.Apply(ctx, key+":tickets", costs, nil)
			return e
		},
		Health: func(ctx command.Context, c gamedata.RogueCostume, growth float64) (uint64, error) {
			contributions, e := p.pictorialService.Contributions(ctx)
			if e != nil {
				return 0, e
			}
			awake, e := p.charAwakeService.Contributions(ctx, roster.Character{ID: c.Character})
			if e != nil {
				return 0, e
			}
			contributions = append(contributions, awake...)
			contributions = append(contributions, gamedata.StatContribution{Stat: gamedata.StatHealth, Percent: growth})
			hp := gamedata.AggregateStats(gamedata.BaseStats{Health: float64(c.Health)}, contributions).Health
			return uint64(hp), nil
		},
		Ranking: p.rogueRanking(),
		Publish: func(ctx command.Context, season, score, damage, version uint64) error {
			profile, e := p.PublicProfile(ctx, owner)
			if e != nil {
				return e
			}
			xp, e := p.missionService.AchievementExperience()
			if e != nil {
				return e
			}
			raw := wire.AppendString(nil, 2, profile.UserID)
			raw = wire.AppendVarint(raw, 3, xp)
			raw = wire.AppendVarint(raw, 4, profile.PortraitCostumeID)
			raw = wire.AppendVarint(raw, 5, profile.PortraitDesignID)
			raw = wire.AppendVarint(raw, 9, profile.TitleID)
			if len(profile.GuildBase) > 0 {
				raw = wire.AppendBytes(raw, 6, profile.GuildBase)
			}
			payload, e := json.Marshal(roguelike.RankPublication{Account: ctx.AccountID, Season: season, Version: version, Entry: roguelike.RankEntry{Owner: owner, Score: score, Damage: damage, Profile: raw}})
			if e != nil {
				return e
			}
			return ctx.Publish(command.Event{ID: fmt.Sprintf("roguelike-rank:%d:%d", season, version), Topic: "roguelike.ranking", Payload: payload})
		},
	})
	if e != nil {
		return e
	}
	p.rogueLikeService = service
	p.battleService.AttachEventBattle(service)
	return nil
}

func (p *playerAssembly) rogueRanking() roguelike.Ranking {
	if p.shared == nil {
		return nil
	}
	return p.shared.rogueRanks
}
