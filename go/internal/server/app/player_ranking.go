package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
)

func huntRankingChanged(requests []player.Request) bool {
	for _, request := range requests {
		switch request.Path {
		case "/BattleEnd", "/MonsterHuntQuickBattle", "/UserNicknameChange", "/UserTitleChange", "/UserPortraitChange", "/AchievementClear":
			return true
		}
	}
	return false
}

func (p *playerAssembly) publishHuntRanking(ctx command.Context) error {
	if p.shared == nil || p.shared.huntRanks == nil {
		return nil
	}
	owner, err := p.ProfileOwner(ctx)
	if err != nil {
		return err
	}
	records, err := p.monsterHuntService.RankRecords()
	if err != nil || len(records) == 0 {
		return err
	}
	profile, err := p.PublicProfile(ctx, owner)
	if err != nil {
		return err
	}
	xp, err := p.missionService.AchievementExperience()
	if err != nil {
		return err
	}
	next := monsterhunt.RankPublication{Account: ctx.AccountID, Records: records, Profile: monsterhunt.RankProfile{
		Owner: owner, Name: profile.UserID, XP: xp, Portrait: profile.PortraitCostumeID, PortraitDesign: profile.PortraitDesignID, Title: profile.TitleID, Guild: profile.GuildBase,
	}}
	oldRaw, found, err := ctx.State.LoadEntry("directory", "hunt_publication", "current")
	if err != nil {
		return err
	}
	if found {
		var old monsterhunt.RankPublication
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
		return errors.New("hunt publication version exhausted")
	}
	next.Version++
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := ctx.Publish(command.Event{ID: fmt.Sprintf("hunt-ranking:%d", next.Version), Topic: "monster_hunt.ranking", Payload: raw}); err != nil {
		return err
	}
	return ctx.State.PutEntry("directory", "hunt_publication", "current", raw)
}

func (s *sharedRuntime) consumeHuntRanking(ctx context.Context, event outbox.Pending) error {
	var publication monsterhunt.RankPublication
	if err := json.Unmarshal(event.Envelope.Event.Payload, &publication); err != nil {
		return err
	}
	if publication.Account != event.Envelope.Source {
		return errors.New("hunt ranking source mismatch")
	}
	return s.huntRanks.Publish(ctx, publication)
}

func (p *playerInstance) totalRanking(ctx command.Context, request []byte) (int, []byte, error) {
	const code = 518
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 1 || field.Type != 0 {
			return errors.New("ranking: invalid request")
		}
		return nil
	}); err != nil {
		return code, nil, err
	}
	var out []byte
	appendRows := func(field int, rows [][]byte) {
		for _, row := range rows {
			out = wire.AppendBytes(out, field, row)
		}
	}
	if service := p.assembly.pvpService; service != nil {
		season, rows, err := service.RankingSnapshot(pvp.Mirror, 3)
		if err != nil {
			return code, nil, err
		}
		appendRows(1, rows)
		out = wire.AppendVarint(out, 6, season)
		season, rows, err = service.RankingSnapshot(pvp.Colosseum, 3)
		if err != nil {
			return code, nil, err
		}
		appendRows(5, rows)
		out = wire.AppendVarint(out, 9, season)
	}
	season := p.assembly.monsterHuntService.CurrentSeason()
	out = wire.AppendVarint(out, 7, season)
	if shared := p.factory.shared; shared != nil && shared.huntRanks != nil {
		for _, entry := range shared.huntRanks.Top(season, 3) {
			out = wire.AppendBytes(out, 2, monsterhunt.RankWire(entry))
		}
		service, err := p.socialService()
		if err != nil {
			return code, nil, err
		}
		cancel := ctx.Cancellation
		if cancel == nil {
			cancel = context.Background()
		}
		season, rows, history, err := service.RankingSnapshot(cancel, 3)
		if err != nil {
			return code, nil, err
		}
		out = wire.AppendVarint(out, 8, season)
		appendRows(3, rows)
		appendRows(4, history)
	}
	return code, out, nil
}
