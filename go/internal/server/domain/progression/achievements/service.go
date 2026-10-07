package achievements

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

type AchievementService struct {
	design *gamedata.AchievementCounterDesign
	store  stateio.ScopedEntryStore

	claims AchievementClaimSource
}
type AchievementClaimSource interface {
	ClaimedAchievementIDs() map[gamedata.AchievementKey]bool
}
type achievementReceipt struct {
	Sequence uint64 `json:"sequence"`
	Group    int    `json:"group"`
	Add      int    `json:"add"`
}
type achievementSnapshot struct {
	Counts   map[string]int64              `json:"counts"`
	Receipts map[string]achievementReceipt `json:"receipts"`
}

func NewAchievementService(design *gamedata.AchievementCounterDesign, store stateio.Store, claims ...AchievementClaimSource) (*AchievementService, error) {
	if design == nil || len(design.Groups) == 0 || store == nil {
		return nil, fmt.Errorf("achievement: missing design or storage")
	}
	entries, ok := store.(stateio.ScopedEntryStore)
	if !ok {
		return nil, fmt.Errorf("achievement: storage requires atomic entries")
	}
	service := &AchievementService{design: design, store: entries}
	if len(claims) > 0 {
		service.claims = claims[0]
	}
	return service, nil
}

// Load each request from the transaction snapshot. A rolled-back request must
// never leave an increment or replay receipt in an in-memory cache.
func (s *AchievementService) load(ctx command.Context) (achievementSnapshot, error) {
	state := achievementSnapshot{Counts: map[string]int64{}, Receipts: map[string]achievementReceipt{}}
	counts, err := s.store.ListEntries(ctx.State, "missions", "achievement_counts")
	if err != nil {
		return state, err
	}
	for key, raw := range counts {
		var value int64
		if err = json.Unmarshal(raw, &value); err != nil {
			return state, fmt.Errorf("achievement: invalid counter: %w", err)
		}
		state.Counts[key] = value
	}
	if ctx.SessionID != "" {
		raw, found, err := s.store.LoadEntry(ctx.State, "missions", "achievement_receipts", ctx.SessionID)
		if err != nil {
			return state, err
		}
		if found {
			var receipt achievementReceipt
			if err = json.Unmarshal(raw, &receipt); err != nil {
				return state, fmt.Errorf("achievement: invalid receipt: %w", err)
			}
			state.Receipts[ctx.SessionID] = receipt
		}
	}
	if state.Counts == nil || state.Receipts == nil {
		return state, fmt.Errorf("achievement: incomplete state")
	}
	for key, value := range state.Counts {
		group, err := strconv.Atoi(key)
		if err != nil || len(s.design.Groups[group]) == 0 || value < 0 {
			return state, fmt.Errorf("achievement: invalid persisted counter")
		}
	}
	return state, nil
}

var ErrInvalidRequest = errors.New("world: invalid protobuf request")
