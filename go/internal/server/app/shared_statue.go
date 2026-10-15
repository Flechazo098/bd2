package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"bd2server/internal/server/domain/command"
	eventactions "bd2server/internal/server/domain/events/actions"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

func statueCount(tx *shared.Tx, key string) (uint64, error) {
	raw, found, err := tx.Get("statue_hits", key)
	if err != nil || !found {
		return 0, err
	}
	var n uint64
	err = json.Unmarshal(raw, &n)
	if err == nil && n > math.MaxInt64 {
		err = fmt.Errorf("invalid shared statue count")
	}
	return n, err
}

func (s *sharedRuntime) consumeStatueHits(ctx context.Context, event outbox.Pending) error {
	var update eventactions.StatueHits
	if err := json.Unmarshal(event.Envelope.Event.Payload, &update); err != nil {
		return err
	}
	if update.ID == 0 || update.Count > math.MaxInt64 {
		return fmt.Errorf("invalid statue publication")
	}
	return s.store.Consume(ctx, event, func(tx *shared.Tx, _ outbox.Envelope) error {
		id := strconv.FormatUint(update.ID, 10)
		owner := event.Envelope.Source + ":" + id
		before, err := statueCount(tx, owner)
		if err != nil {
			return err
		}
		if update.Count <= before {
			return nil
		}
		total, err := statueCount(tx, id)
		if err != nil {
			return err
		}
		delta := update.Count - before
		if delta > math.MaxInt64-total {
			return fmt.Errorf("statue count overflow")
		}
		raw, err := json.Marshal(total + delta)
		if err != nil {
			return err
		}
		if err = tx.Put("statue_hits", id, raw); err != nil {
			return err
		}
		raw, err = json.Marshal(update.Count)
		if err != nil {
			return err
		}
		return tx.Put("statue_hits", owner, raw)
	})
}

func (s *sharedRuntime) statueHits(ctx command.Context, id uint64) (uint64, uint64, error) {
	cancel := ctx.Cancellation
	if cancel == nil {
		cancel = context.Background()
	}
	var total, personal uint64
	err := s.store.View(cancel, func(tx *shared.Tx) error {
		var err error
		key := strconv.FormatUint(id, 10)
		total, err = statueCount(tx, key)
		if err != nil {
			return err
		}
		personal, err = statueCount(tx, ctx.AccountID+":"+key)
		return err
	})
	return total, personal, err
}
