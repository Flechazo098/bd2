package events

import (
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

func (e *Economy) AttachPrestigeSkins(design map[uint64]uint64)  { e.prestige = design }
func (e *Economy) AttachPrestigePortrait(portrait func() uint64) { e.prestigePortrait = portrait }

type prestigeSkinReceipt struct{ Request, Response []byte }
type prestigeSkinState struct {
	Selections map[uint64]uint64              `json:"selections"`
	Receipts   map[string]prestigeSkinReceipt `json:"receipts"`
}

func (e *Economy) loadPrestigeSkins() (prestigeSkinState, error) {
	s := prestigeSkinState{Selections: map[uint64]uint64{}, Receipts: map[string]prestigeSkinReceipt{}}
	raw, err := e.store.Load("prestige_skin_sets")
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
func (e *Economy) PrestigeSkinSelections() (map[uint64]uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.loadPrestigeSkins()
	return s.Selections, err
}

func (e *Economy) Handle(path string, request []byte) (int, []byte, bool, error) {
	return e.HandleSession(path, request, "local")
}
func (e *Economy) HandleSession(path string, req []byte, session string) (int, []byte, bool, error) {
	if path != "/PrestigeSkinSet" {
		return e.PrestigeSkinInfo(path, req)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := wire.Walk(req, func(f wire.Field) error {
		if (f.Number == 1 && f.Type != 0) || (f.Number == 2 && f.Type != 2) {
			return fmt.Errorf("events: invalid prestige request field")
		}
		return nil
	}); err != nil {
		return 426, nil, true, err
	}
	seq, _, err := wire.Varint(req, 1)
	info, found, err2 := wire.Bytes(req, 2)
	if err != nil || err2 != nil || !found || seq == 0 || seq > math.MaxInt32 || session == "" {
		return 426, nil, true, fmt.Errorf("events: invalid prestige set request")
	}
	if err := wire.Walk(info, func(f wire.Field) error {
		if f.Number >= 1 && f.Number <= 4 && f.Type != 0 {
			return fmt.Errorf("events: invalid prestige info field")
		}
		return nil
	}); err != nil {
		return 426, nil, true, err
	}
	costume, _, err := wire.Varint(info, 1)
	design, _, err2 := wire.Varint(info, 2)
	setting, _, err3 := wire.Varint(info, 3)
	timeValue, _, err4 := wire.Varint(info, 4)
	if err != nil || err2 != nil || err3 != nil || err4 != nil || costume == 0 || costume > math.MaxInt32 || design == 0 || design > math.MaxInt32 || setting > 1 || timeValue > math.MaxInt64 || e.prestige[design] != costume {
		return 426, nil, true, fmt.Errorf("events: invalid prestige skin")
	}
	s, err := e.loadPrestigeSkins()
	if err != nil {
		return 426, nil, true, err
	}
	key := session + ":" + strconv.FormatUint(seq, 10)
	if receipt, ok := s.Receipts[key]; ok {
		if !bytes.Equal(receipt.Request, req) {
			return 426, nil, true, fmt.Errorf("events: prestige sequence conflict")
		}
		return 426, append([]byte(nil), receipt.Response...), true, nil
	}
	owned := false
	for _, item := range e.items.All() {
		if item.Type == 45 && item.ID == design && item.Count > 0 {
			owned = true
			break
		}
	}
	if !owned {
		return 426, nil, true, fmt.Errorf("events: prestige skin not owned")
	}
	if setting == 1 {
		s.Selections[costume] = design
	} else if s.Selections[costume] == design {
		delete(s.Selections, costume)
	}
	portrait := costume
	if e.prestigePortrait != nil {
		portrait = e.prestigePortrait()
	}
	out := wire.AppendVarint(nil, 1, portrait)
	out = wire.AppendVarint(out, 2, s.Selections[portrait])
	s.Receipts[key] = prestigeSkinReceipt{Request: append([]byte(nil), req...), Response: append([]byte(nil), out...)}
	raw, err := json.Marshal(s)
	if err == nil {
		err = e.store.Save("prestige_skin_sets", raw)
	}
	if err != nil {
		return 426, nil, true, err
	}
	return 426, out, true, nil
}
func (e *Economy) PrestigeSkinInfo(path string, req []byte) (int, []byte, bool, error) {
	if path != "/PrestigeSkinInfo" {
		return 0, nil, false, nil
	}
	seq, _, err := wire.Varint(req, 1)
	if err != nil || seq == 0 || seq > math.MaxInt32 {
		return 425, nil, true, fmt.Errorf("events: missing sequence")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.loadPrestigeSkins()
	if err != nil {
		return 425, nil, true, err
	}
	var out []byte
	seen := map[uint64]bool{}
	for _, item := range e.items.All() {
		if item.Type != 45 || item.Count == 0 || seen[item.ID] {
			continue
		}
		costume, ok := e.prestige[item.ID]
		if !ok {
			return 425, nil, true, fmt.Errorf("events: owned skin missing design")
		}
		seen[item.ID] = true
		b := wire.AppendVarint(nil, 1, costume)
		b = wire.AppendVarint(b, 2, item.ID)
		if s.Selections[costume] == item.ID {
			b = wire.AppendVarint(b, 3, 1)
		}
		b = wire.AppendVarint(b, 4, item.TimeValue)
		out = wire.AppendBytes(out, 1, b)
	}
	return 425, out, true, nil
}
