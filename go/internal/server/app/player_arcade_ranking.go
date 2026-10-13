package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
)

func arcadeRankingChanged(requests []player.Request) bool {
	for _, request := range requests {
		if strings.HasPrefix(request.Path, "/MiniGame") && strings.HasSuffix(request.Path, "End") {
			return true
		}
		switch request.Path {
		case "/UserNicknameChange", "/UserTitleChange", "/UserPortraitChange", "/AchievementClear":
			return true
		}
	}
	return false
}

func (p *playerAssembly) arcadeRankingGuilds(ctx command.Context, owners []uint64) (map[uint64][]byte, error) {
	accounts := make([]string, 0, len(owners))
	byOwner := make(map[uint64]string, len(owners))
	own, err := p.ProfileOwner(ctx)
	if err != nil {
		return nil, err
	}
	for _, owner := range owners {
		account := p.shared.arcadeRanks.Account(owner)
		if owner == own {
			account = ctx.AccountID
		}
		if account == "" {
			continue
		}
		byOwner[owner] = account
		accounts = append(accounts, account)
	}
	values, err := p.shared.guild.GuildBases(context.Background(), accounts)
	if err != nil {
		return nil, err
	}
	result := make(map[uint64][]byte, len(byOwner))
	for owner, account := range byOwner {
		result[owner] = values[account]
	}
	return result, nil
}

func (p *playerAssembly) arcadeRankingProfile(ctx command.Context) (ranking.ArcadeProfile, error) {
	owner, err := p.ProfileOwner(ctx)
	if err != nil {
		return ranking.ArcadeProfile{}, err
	}
	profile, err := p.PublicProfile(ctx, owner)
	if err != nil {
		return ranking.ArcadeProfile{}, err
	}
	xp, err := p.missionService.AchievementExperience()
	if err != nil {
		return ranking.ArcadeProfile{}, err
	}
	return ranking.ArcadeProfile{Owner: owner, Name: profile.UserID, XP: xp, Portrait: profile.PortraitCostumeID, PortraitDesign: profile.PortraitDesignID, Title: profile.TitleID, Guild: profile.GuildBase}, nil
}

func (p *playerAssembly) publishArcadeRanking(ctx command.Context) error {
	if p.shared == nil || p.shared.arcadeRanks == nil || p.eventPlayService == nil {
		return nil
	}
	records := p.eventPlayService.RankRecords()
	if len(records) == 0 {
		return nil
	}
	profile, err := p.arcadeRankingProfile(ctx)
	if err != nil {
		return err
	}
	next := ranking.ArcadePublication{Account: ctx.AccountID, Profile: profile, Records: records}
	oldRaw, found, err := ctx.State.LoadEntry("directory", "arcade_publication", "current")
	if err != nil {
		return err
	}
	if found {
		var old ranking.ArcadePublication
		if err := json.Unmarshal(oldRaw, &old); err != nil {
			return err
		}
		next.Version = old.Version
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if bytes.Equal(raw, oldRaw) {
			return nil
		}
	}
	if next.Version == ^uint64(0) {
		return errors.New("arcade publication version exhausted")
	}
	next.Version++
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := ctx.Publish(command.Event{ID: fmt.Sprintf("arcade-ranking:%d", next.Version), Topic: "arcade.ranking", Payload: raw}); err != nil {
		return err
	}
	return ctx.State.PutEntry("directory", "arcade_publication", "current", raw)
}

func (s *sharedRuntime) consumeArcadeRanking(ctx context.Context, event outbox.Pending) error {
	var publication ranking.ArcadePublication
	if err := json.Unmarshal(event.Envelope.Event.Payload, &publication); err != nil {
		return err
	}
	if publication.Account != event.Envelope.Source {
		return errors.New("arcade ranking source mismatch")
	}
	if event.Envelope.Event.ID != fmt.Sprintf("arcade-ranking:%d", publication.Version) {
		return errors.New("arcade ranking event identity mismatch")
	}
	return s.arcadeRanks.Publish(ctx, publication)
}
