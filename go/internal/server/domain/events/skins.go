package events

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
)

func (e *Economy) AttachPrestigeSkins(design map[uint64]uint64)  { e.prestige = design }
func (e *Economy) AttachPrestigePortrait(portrait func() uint64) { e.prestigePortrait = portrait }

type prestigeSkinReceipt struct{ Request, Response []byte }
type prestigeSkinState struct {
	Selections map[uint64]uint64              `json:"selections"`
	Receipts   map[string]prestigeSkinReceipt `json:"receipts"`
}

func (e *Economy) loadPrestigeSkins(ctx command.Context) (prestigeSkinState, error) {
	s := prestigeSkinState{Selections: map[uint64]uint64{}, Receipts: map[string]prestigeSkinReceipt{}}
	raw, err := e.store.Load(ctx.State, "prestige_skin_sets")
	if err != nil || raw == nil {
		return s, err
	}
	if err = stateio.RequireExactJSONObject(raw, "selections", "receipts"); err != nil {
		return s, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	if s.Selections == nil || s.Receipts == nil {
		return s, fmt.Errorf("events: invalid saved prestige skins")
	}
	for costume, design := range s.Selections {
		if costume == 0 || design == 0 || e.prestige[design] != costume {
			return s, fmt.Errorf("events: invalid saved prestige selection")
		}
	}
	return s, nil
}

// PrestigeSkinSelections returns a detached projection for costume responses.
func (e *Economy) PrestigeSkinSelections(ctx command.Context) (map[uint64]uint64, error) {

	s, err := e.loadPrestigeSkins(ctx)
	return s.Selections, err
}
