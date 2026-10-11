package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/fishing"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

type publicFishing struct {
	Account         string
	Owner           uint64
	Name            string
	Profile         fishing.Profile
	CollectionCount uint64
}

type directoryRecord struct {
	Version uint64
	Player  publicFishing
}
type publicDirectory struct {
	store   *shared.Store
	players sync.Map
}

func openPublicDirectory(ctx context.Context, store *shared.Store) (*publicDirectory, error) {
	d := &publicDirectory{store: store}
	err := store.View(ctx, func(tx *shared.Tx) error {
		for cursor := ""; ; {
			rows, err := tx.Scan("player.directory", "", cursor, 256)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				var record directoryRecord
				if err := json.Unmarshal(row.Value, &record); err != nil {
					return err
				}
				if record.Player.Owner == 0 || record.Version == 0 {
					return errors.New("invalid public player directory")
				}
				d.players.Store(record.Player.Owner, record)
				cursor = row.Key
			}
		}
	})
	return d, err
}

func (p *playerAssembly) publishDirectory(ctx command.Context) error {
	if p.shared == nil {
		return nil
	}
	owner, err := p.ProfileOwner(ctx)
	if err != nil {
		return err
	}
	next := directoryRecord{Player: publicFishing{Account: ctx.AccountID, Owner: owner, Name: p.nicknameService.CurrentNickname(), Profile: p.fishingService.Profile(), CollectionCount: p.fishingService.CollectionCount()}}
	raw, found, err := ctx.State.LoadEntry("directory", "publication", "current")
	if err != nil {
		return err
	}
	if found {
		var old directoryRecord
		if err := json.Unmarshal(raw, &old); err != nil {
			return err
		}
		if reflect.DeepEqual(old.Player, next.Player) {
			return nil
		}
		next.Version = old.Version
	}
	if next.Version == ^uint64(0) {
		return errors.New("public directory version exhausted")
	}
	next.Version++
	raw, err = json.Marshal(next)
	if err != nil {
		return err
	}
	if err := ctx.Publish(command.Event{ID: fmt.Sprintf("directory:%d", next.Version), Topic: "player.directory", Payload: raw}); err != nil {
		return err
	}
	return ctx.State.PutEntry("directory", "publication", "current", raw)
}

func (d *publicDirectory) consume(ctx context.Context, event outbox.Pending) error {
	var next directoryRecord
	if err := json.Unmarshal(event.Envelope.Event.Payload, &next); err != nil {
		return err
	}
	if next.Version == 0 || next.Player.Account != event.Envelope.Source || next.Player.Owner == 0 {
		return errors.New("public player directory source mismatch")
	}
	err := d.store.Consume(ctx, event, func(tx *shared.Tx, _ outbox.Envelope) error {
		key := strconv.FormatUint(next.Player.Owner, 10)
		raw, found, err := tx.Get("player.directory", key)
		if err != nil {
			return err
		}
		if found {
			var old directoryRecord
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
			if old.Player.Account != next.Player.Account {
				return errors.New("public player ownership conflict")
			}
			if old.Version >= next.Version {
				next = old
				return nil
			}
		}
		payload, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return tx.Put("player.directory", key, payload)
	})
	if err == nil {
		d.players.Store(next.Player.Owner, next)
	}
	return err
}

func (d *publicDirectory) fishing(_ command.Context, owner uint64) (string, fishing.Profile, uint64, error) {
	value, ok := d.players.Load(owner)
	if !ok {
		return "", fishing.Profile{}, 0, errors.New("public player not found")
	}
	p := value.(directoryRecord).Player
	p.Profile.Maps = append([]uint64(nil), p.Profile.Maps...)
	return p.Name, p.Profile, p.CollectionCount, nil
}
