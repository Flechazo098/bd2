package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
	"context"
	"encoding/json"
	"fmt"
)

type rankingStore struct{ store *shared.Store }
type rankingTx struct{ *shared.Tx }

func (s rankingStore) View(ctx context.Context, read func(ranking.Tx) error) error {
	return s.store.View(ctx, func(tx *shared.Tx) error { return read(rankingTx{tx}) })
}
func (s rankingStore) Update(ctx context.Context, write func(ranking.Tx) error) error {
	return s.store.Update(ctx, func(tx *shared.Tx) error { return write(rankingTx{tx}) })
}
func (t rankingTx) Scan(bucket, prefix, after string, limit int) ([]ranking.Entry, error) {
	entries, err := t.Tx.Scan(bucket, prefix, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ranking.Entry, len(entries))
	for i, row := range entries {
		out[i] = ranking.Entry{Key: row.Key, Value: row.Value}
	}
	return out, nil
}
func newSharedPVP(ctx context.Context, store *shared.Store, design *gamedata.PvpDesign, seasons []pvp.Season) (*ranking.PVP, error) {
	return ranking.NewPVP(ctx, rankingStore{store}, design, seasons)
}

func (s *sharedRuntime) consumePVP(ctx context.Context, event outbox.Pending) error {
	if event.Envelope.Event.Topic != "pvp.popularity" {
		if e := s.pvp.Consume(ctx, event.Envelope.Source, event.Envelope.Event); e != nil {
			return e
		}
		if event.Envelope.Event.Topic == "pvp.battle.settled" && s.statistics != nil {
			var battle pvp.BattleSettled
			if e := json.Unmarshal(event.Envelope.Event.Payload, &battle); e != nil {
				return e
			}
			if battle.Mode != pvp.Mirror {
				return nil
			}
			return s.store.Consume(ctx, event, func(tx *shared.Tx, envelope outbox.Envelope) error {
				for side, snapshot := range []pvp.PublishedSnapshot{battle.Attacker, battle.Defender} {
					if e := s.applyPVPPopularity(tx, snapshot.Profile.OwnerID, pvp.PopularityPublished{Timestamp: battle.Timestamp, Side: uint64(side), Snapshot: snapshot}); e != nil {
						return e
					}
				}
				return nil
			})
		}
		return nil
	}
	var update pvp.PopularityPublished
	if json.Unmarshal(event.Envelope.Event.Payload, &update) != nil || update.Timestamp == 0 || update.Side > 1 || update.Snapshot.Profile.OwnerID != event.Envelope.Source {
		return fmt.Errorf("invalid PvP popularity publication")
	}
	if s.statistics == nil {
		return fmt.Errorf("missing popularity design")
	}
	if update.Snapshot.Mode != pvp.Mirror {
		return nil
	}
	return s.store.Consume(ctx, event, func(tx *shared.Tx, envelope outbox.Envelope) error {
		return s.applyPVPPopularity(tx, envelope.Source, update)
	})
}

func (s *sharedRuntime) applyPVPPopularity(tx *shared.Tx, owner string, update pvp.PopularityPublished) error {
	id, e := s.pvp.PopularityRank(update.Snapshot)
	if e != nil {
		return e
	}
	group := s.statistics.PVPRankGroups[id]
	if e := rotatePopularity(tx, update.Timestamp); e != nil {
		return e
	}
	source := recommendationSource(owner)
	identity := fmt.Sprintf("pvp:%s:%d", source, update.Side)
	raw, found, e := tx.Get("popularity_pvp_revision", fmt.Sprintf("%s:%d", identity, update.Snapshot.Season))
	if e != nil {
		return e
	}
	var latest uint64
	if found && json.Unmarshal(raw, &latest) != nil {
		return fmt.Errorf("invalid PvP popularity revision")
	}
	if latest > update.Snapshot.Version {
		return nil
	}
	next := map[string]int64{}
	costumes := map[uint64]uint64{}
	if e = wire.Walk(update.Snapshot.Snapshot.FullDeck, func(f wire.Field) error {
		if f.Number != 3 {
			return nil
		}
		index, _, e := wire.Varint(f.Value, 1)
		if e != nil {
			return e
		}
		id, _, e := wire.Varint(f.Value, 2)
		costumes[index] = id
		return e
	}); e != nil {
		return e
	}
	if group != 0 && group <= s.statistics.PVPMinimumGroup {
		if e = wire.Walk(update.Snapshot.Snapshot.FullDeck, func(f wire.Field) error {
			if f.Number != 1 {
				return nil
			}
			order := uint64(0)
			return wire.Walk(f.Value, func(skill wire.Field) error {
				if skill.Number != 5 {
					return nil
				}
				index, _, e := wire.Varint(skill.Value, 1)
				if e != nil {
					return e
				}
				costume := costumes[index]
				if costume == 0 {
					return fmt.Errorf("invalid PvP popularity costume")
				}
				for _, filter := range []uint64{0, group} {
					key := "pvp:" + popularityContext(0, update.Side, order, filter, 0) + fmt.Sprintf("%010d", costume)
					next[key]++
				}
				order++
				return nil
			})
		}); e != nil {
			return e
		}
	}
	if e = popularityReplace(tx, identity, next); e != nil {
		return e
	}
	raw, e = json.Marshal(update.Snapshot.Version)
	if e != nil {
		return e
	}
	if e = tx.Put("popularity_pvp_revision", fmt.Sprintf("%s:%d", identity, update.Snapshot.Season), raw); e != nil {
		return e
	}
	r := deck.RecommendRecord{}
	if e = decodePopularityEquipment(update.Snapshot.Snapshot.FullDeck, &r); e != nil {
		return e
	}
	return s.equipmentPopularity(tx, source, &r)
}
