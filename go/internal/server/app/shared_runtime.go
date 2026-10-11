package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/runtime/delivery"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

type sharedRuntime struct {
	directory *publicDirectory
	rooms     *roomResults
	store     *shared.Store
	pvp       *ranking.PVP
	guild     *social.Owner
	friends   *social.Friends
	delivery  atomic.Pointer[delivery.Dispatcher]
	cancel    context.CancelFunc
	done      chan struct{}
}

func openSharedRuntime(ctx context.Context, c *configuration, design *designCatalog) (*sharedRuntime, error) {
	store, err := shared.Open(filepath.Join(c.stateDirectory, "shared"))
	if err != nil {
		return nil, err
	}
	pvp, err := newSharedPVP(ctx, store, design.pvp.Design, design.pvp.Seasons)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	guild, err := social.New(socialStore{store}, design.guild)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	friends, err := social.NewFriends(socialStore{store}, design.friends)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	rooms, err := openRoomResults(ctx, store)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	directory, err := openPublicDirectory(ctx, store)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	return &sharedRuntime{store: store, pvp: pvp, guild: guild, friends: friends, rooms: rooms, directory: directory}, nil
}

func (s *sharedRuntime) wake(account string) {
	if d := s.delivery.Load(); d != nil {
		d.Wake(account)
	}
}

func (s *sharedRuntime) Consume(ctx context.Context, event outbox.Pending) error {
	if _, _, err := outbox.Validate(event); err != nil {
		return err
	}
	if event.Envelope.Event.Topic == "fishing.projection" {
		return s.rooms.updateFishing(ctx, event)
	}
	if event.Envelope.Event.Topic == "player.directory" {
		return s.directory.consume(ctx, event)
	}
	if event.Envelope.Event.Topic == "friend.directory" {
		var profile social.FriendProfile
		if err := json.Unmarshal(event.Envelope.Event.Payload, &profile); err != nil {
			return err
		}
		if profile.AccountID != event.Envelope.Source {
			return social.ErrPermission
		}
		return s.friends.Publish(ctx, profile)
	}
	if event.Envelope.Event.Topic == "chat.policy" {
		return s.consumeChatPolicy(ctx, event.Envelope.Source, event.Envelope.Event.Payload)
	}
	if strings.HasPrefix(event.Envelope.Event.Topic, "room.run.") && s.rooms != nil {
		return s.rooms.Consume(ctx, event)
	}
	if strings.HasPrefix(event.Envelope.Event.Topic, "pvp.") {
		return s.pvp.Consume(ctx, event.Envelope.Source, event.Envelope.Event)
	}
	if event.Envelope.Event.Topic == "social.confirm" {
		var proof social.CommitProof
		if err := json.Unmarshal(event.Envelope.Event.Payload, &proof); err != nil {
			return err
		}
		if proof.ActorAccount != event.Envelope.Source || proof.OperationID != event.Envelope.Event.ID {
			return errors.New("guild confirmation source mismatch")
		}
		_, err := s.guild.Confirm(ctx, proof)
		return err
	}
	if event.Envelope.Event.Topic == "social.raid_settle" {
		var result social.RaidResult
		if err := json.Unmarshal(event.Envelope.Event.Payload, &result); err != nil {
			return err
		}
		if result.AccountID != event.Envelope.Source || result.ResultID != event.Envelope.Event.ID {
			return errors.New("guild raid result source mismatch")
		}
		_, err := s.guild.SettleRaid(ctx, result)
		return err
	}
	return fmt.Errorf("unsupported shared event topic %q", event.Envelope.Event.Topic)
}

func (s *sharedRuntime) start(registry *playerRegistry, discovery outboxDiscovery) error {
	d, err := delivery.New(registry, s, discovery)
	if err != nil {
		return err
	}
	s.delivery.Store(d)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			if err := s.settle(ctx, registry, discovery); err != nil && ctx.Err() == nil {
				slog.Warn("shared settlement deferred", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (s *sharedRuntime) stop() error {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
	if d := s.delivery.Load(); d != nil {
		return d.Close(context.Background())
	}
	return nil
}

func (s *sharedRuntime) settle(ctx context.Context, registry *playerRegistry, discovery outboxDiscovery) error {
	if err := s.closeExpiredRaids(ctx, registry); err != nil {
		return err
	}
	operations, _, err := s.guild.PendingOperations(ctx, "", 128)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		raw, err := json.Marshal(operation)
		if err != nil {
			return err
		}
		if _, err = executePlayerSystem(ctx, registry, operation.ActorAccount, player.ReconcileSocial, raw); err != nil {
			return err
		}
	}
	for cursor := ""; ; {
		guilds, next, err := s.guild.DueDeletions(ctx, time.Now().UnixMilli(), cursor, 128)
		if err != nil {
			return err
		}
		for _, guild := range guilds {
			if err := s.guild.FinalizeDeletion(ctx, guild, time.Now().UnixMilli()); err != nil && !errors.Is(err, social.ErrPending) && !errors.Is(err, social.ErrIneligible) && !errors.Is(err, social.ErrSeasonLocked) {
				return err
			}
		}
		if len(guilds) < 128 || next == cursor {
			break
		}
		cursor = next
	}
	for _, season := range registry.factory.design.pvp.Seasons {
		if uint64(time.Now().UnixMilli()) < season.End {
			continue
		}
		var accounts []string
		for cursor := ""; ; {
			page, err := discovery.Accounts(ctx, cursor, 128)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			for _, account := range page {
				if account <= cursor {
					return errors.New("nonadvancing settlement account cursor")
				}
				accounts = append(accounts, account)
				cursor = account
			}
		}
		if err := s.pvp.BeginClosing(ctx, season, accounts); err != nil {
			return err
		}
	}
	for _, closure := range s.pvp.RequiredClosures(128) {
		raw, err := json.Marshal(closure)
		if err != nil {
			return err
		}
		if _, err := executePlayerSystem(ctx, registry, closure.Owner, player.ClosePVPSeason, raw); err != nil {
			return err
		}
	}
	if err := s.pvp.FreezeDue(ctx, time.Now()); err != nil {
		return err
	}
	for _, content := range registry.factory.options.calendars.RegularService.Contents {
		if content.ID != 6 {
			continue
		}
		if uint64(time.Now().UnixMilli()) >= content.Current.EndMilliseconds+registry.factory.options.calendars.RegularService.CalculateMilliseconds {
			if err := s.guild.FinalizeSeason(ctx, content.Current.ID, time.Now().UnixMilli()); err != nil && !errors.Is(err, social.ErrPending) {
				return err
			}
		}
	}
	return nil
}

func (s *sharedRuntime) closeExpiredRaids(ctx context.Context, registry *playerRegistry) error {
	for cursor := ""; ; {
		entries, next, err := s.guild.PendingRaidEntries(ctx, cursor, 128)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			season, err := s.guild.Season(ctx, entry.GuildID, entry.Season)
			if err != nil {
				return err
			}
			if time.Now().UnixMilli() < season.EndsMS {
				continue
			}
			if _, err := executePlayerSystem(ctx, registry, entry.AccountID, player.CloseExpiredRaid, nil); err != nil {
				return err
			}
		}
		if next == "" || next == cursor {
			return nil
		}
		cursor = next
	}
}
