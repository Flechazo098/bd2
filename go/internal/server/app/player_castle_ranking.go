package app

import (
	"bd2server/internal/server/domain/battle/castle"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/outbox"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

func (p *playerAssembly) publishCastleRanking(ctx command.Context) error {
	if p.shared == nil || p.shared.castleRanks == nil {
		return nil
	}
	owner, err := p.ProfileOwner(ctx)
	if err != nil {
		return err
	}
	records, err := p.castleService.RankRecords(ctx)
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
	next := castle.RankPublication{Account: ctx.AccountID, Records: records, Profile: castle.RankProfile{
		Owner: owner, Name: profile.UserID, XP: xp, Portrait: profile.PortraitCostumeID, PortraitDesign: profile.PortraitDesignID, Title: profile.TitleID, Guild: profile.GuildBase,
	}}
	oldRaw, found, err := ctx.State.LoadEntry("directory", "castle_publication", "current")
	if err != nil {
		return err
	}
	if found {
		var old castle.RankPublication
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
		return errors.New("castle publication version exhausted")
	}
	next.Version++
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := ctx.Publish(command.Event{ID: fmt.Sprintf("castle-ranking:%d", next.Version), Topic: "castle.ranking", Payload: raw}); err != nil {
		return err
	}
	return ctx.State.PutEntry("directory", "castle_publication", "current", raw)
}

func (s *sharedRuntime) consumeCastleRanking(ctx context.Context, event outbox.Pending) error {
	var publication castle.RankPublication
	if err := json.Unmarshal(event.Envelope.Event.Payload, &publication); err != nil {
		return err
	}
	if publication.Account != event.Envelope.Source {
		return errors.New("castle ranking source mismatch")
	}
	return s.castleRanks.Publish(ctx, publication)
}
