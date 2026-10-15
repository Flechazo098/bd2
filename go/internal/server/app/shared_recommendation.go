package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

type recommendationVisibility struct {
	Revision uint64
	Public   bool
}
type sharedRecommendation struct {
	Source string
	Record deck.RecommendRecord
}

func recommendationPrefix(c deck.RecommendContext) string {
	return fmt.Sprintf("%010d:%010d:%010d:%010d:%010d:%010d:", c.BattleMode, c.PackID, c.BattleIndex, c.Level, c.TeamCountType, c.DeckType)
}
func recommendationSource(source string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
}
func (s *sharedRuntime) consumeRecommendation(ctx context.Context, event outbox.Pending) error {
	if event.Envelope.Event.Topic != deck.RecommendationTopic {
		return fmt.Errorf("invalid recommendation topic")
	}
	var update deck.RecommendationPublication
	if json.Unmarshal(event.Envelope.Event.Payload, &update) != nil || update.Revision == 0 {
		return fmt.Errorf("invalid recommendation publication")
	}
	if update.Record != nil {
		if !update.Public || update.Record.Revision != update.Revision {
			return fmt.Errorf("invalid recommendation snapshot revision")
		}
		if e := update.Record.Validate(); e != nil {
			return e
		}
	}
	return s.store.Consume(ctx, event, func(tx *shared.Tx, envelope outbox.Envelope) error {
		source := recommendationSource(envelope.Source)
		raw, found, e := tx.Get("recommendation_visibility", source)
		if e != nil {
			return e
		}
		var previous recommendationVisibility
		if found && json.Unmarshal(raw, &previous) != nil {
			return fmt.Errorf("invalid recommendation visibility")
		}
		if update.Revision > previous.Revision {
			raw, e = json.Marshal(recommendationVisibility{Revision: update.Revision, Public: update.Public})
			if e != nil {
				return e
			}
			if e = tx.Put("recommendation_visibility", source, raw); e != nil {
				return e
			}
		}
		if update.Record == nil {
			return nil
		}
		ownerKey := fmt.Sprintf("%020d", update.Record.Identity.OwnerIndex)
		assigned, exists, e := tx.Get("recommendation_owners", ownerKey)
		if e != nil {
			return e
		}
		if exists && string(assigned) != source {
			return fmt.Errorf("recommendation owner source conflict")
		}
		if !exists {
			if e = tx.Put("recommendation_owners", ownerKey, []byte(source)); e != nil {
				return e
			}
		}
		key := recommendationPrefix(update.Record.Context) + ownerKey + ":" + source
		raw, found, e = tx.Get("recommendations", key)
		if e != nil {
			return e
		}
		if found {
			var previous sharedRecommendation
			if json.Unmarshal(raw, &previous) != nil || previous.Source != source {
				return fmt.Errorf("invalid shared recommendation")
			}
			if previous.Record.Revision >= update.Revision {
				return nil
			}
		}
		raw, e = json.Marshal(sharedRecommendation{Source: source, Record: *update.Record})
		if e != nil {
			return e
		}
		return tx.Put("recommendations", key, raw)
	})
}
func (s *sharedRuntime) queryRecommendations(ctx command.Context, scenario deck.RecommendContext) ([]deck.RecommendRecord, error) {
	cancel := ctx.Cancellation
	if cancel == nil {
		cancel = context.Background()
	}
	var records []deck.RecommendRecord
	e := s.store.View(cancel, func(tx *shared.Tx) error {
		prefix := recommendationPrefix(scenario)
		cursor := ""
		for {
			rows, e := tx.Scan("recommendations", prefix, cursor, 256)
			if e != nil {
				return e
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				cursor = row.Key
				var value sharedRecommendation
				if json.Unmarshal(row.Value, &value) != nil || value.Record.Context != scenario {
					return fmt.Errorf("invalid shared recommendation record")
				}
				raw, found, e := tx.Get("recommendation_visibility", value.Source)
				if e != nil {
					return e
				}
				var visibility recommendationVisibility
				if !found || json.Unmarshal(raw, &visibility) != nil {
					return fmt.Errorf("missing recommendation visibility")
				}
				if visibility.Public {
					records = append(records, value.Record)
				}
			}
		}
	})
	return records, e
}
func (p *playerAssembly) attachRecommendationCommunity() error {
	if p.shared == nil {
		return nil
	}
	return p.deckStateStore.AttachRecommendationCommunity(p.shared.queryRecommendations, p.design.costumePotentialDesign.CharacterUnique)
}
