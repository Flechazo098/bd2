package castle

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

func (s *Service) totalWarStart(ctx command.Context, v *state, req []byte) ([]byte, error) {
	if n(req, 2) != 1 || s.validate == nil {
		return nil, fmt.Errorf("castle: total war battle/ownership validation unavailable")
	}
	blue, e := msgs(req, 3)
	if e != nil {
		return nil, e
	}
	red, e := msgs(req, 4)
	if e != nil {
		return nil, e
	}
	if len(blue) == 0 || len(blue) > 2000 || len(red) == 0 || len(red) > 20 {
		return nil, fmt.Errorf("castle: invalid total war teams")
	}
	if s.characters != nil {
		if e = s.validateSavedTotalWarBattle(ctx, v, blue); e != nil {
			return nil, e
		}
	}
	participants := make([]Participant, 0, len(blue))
	seen := map[uint64]bool{}
	unique := map[uint64]bool{}
	var costumes []uint64
	for _, raw := range blue {
		p := Participant{n(raw, 2), n(raw, 6), n(raw, 3), n(raw, 7)}
		attacker := n(raw, 28) == 0
		if p.CharacterIndex == 0 || p.CostumeIndex == 0 || p.CharacterID == 0 || p.CostumeID == 0 || seen[p.CostumeID] || n(raw, 28) > 1 || attacker && (n(raw, 1) == 0 || unique[n(raw, 1)]) || !attacker && n(raw, 1) != 0 {
			return nil, fmt.Errorf("castle: invalid total war participant")
		}
		seen[p.CostumeID] = true
		if attacker {
			unique[n(raw, 1)] = true
		}
		participants = append(participants, p)
		if attacker {
			costumes = append(costumes, p.CostumeID)
		}
	}
	if len(costumes) == 0 || len(costumes) > 20 {
		return nil, fmt.Errorf("castle: invalid total war attacker count")
	}
	if len(red) != len(s.design.TotalWarEnemies) || len(red) == 0 {
		return nil, fmt.Errorf("castle: total war enemies differ from design")
	}
	for i, raw := range red {
		enemy := s.design.TotalWarEnemies[i]
		index := n(raw, 1)
		if index == 0 || unique[index] || n(raw, 2) != 0 || n(raw, 3) != enemy.ID || n(raw, 4) != 1000000 || n(raw, 5) != enemy.Level || n(raw, 9) != uint64(i) || n(raw, 7) != 0 {
			return nil, fmt.Errorf("castle: invalid total war enemy")
		}
		unique[index] = true
	}
	if e = s.validate(ctx, participants); e != nil {
		return nil, e
	}
	v.TotalWarRun = &totalWarRun{Identity: fmt.Sprintf("%s:%d", ctx.SessionID, n(req, 1)), Costumes: costumes, Request: req}
	// Engine V1 runs the client simulation and consumes the registered costume
	// damage report. No verified combat result is fabricated by this service.
	out := wire.AppendVarint(nil, 1, n(req, 1)*7919%math.MaxInt32+1)
	for _, raw := range blue {
		out = wire.AppendBytes(out, 2, raw)
	}
	for _, raw := range red {
		out = wire.AppendBytes(out, 3, raw)
	}
	return out, nil
}

func (s *Service) totalWarEnd(ctx command.Context, v *state, req []byte) ([]byte, error) {
	if v.TotalWarRun == nil {
		return nil, fmt.Errorf("castle: total war settlement without start")
	}
	items, e := msgs(req, 2)
	if e != nil {
		return nil, e
	}
	if len(items) > len(v.TotalWarRun.Costumes) {
		return nil, fmt.Errorf("castle: excess total war scores")
	}
	var scores []score
	seen := map[uint64]bool{}
	var total uint64
	for _, raw := range items {
		id, damage := n(raw, 1), n(raw, 2)
		if !slices.Contains(v.TotalWarRun.Costumes, id) || seen[id] || damage > math.MaxInt64 || total > math.MaxInt64-damage {
			return nil, fmt.Errorf("castle: invalid total war score")
		}
		seen[id] = true
		total += damage
		scores = append(scores, score{id, damage})
	}
	if len(items) != 0 && len(items) != len(v.TotalWarRun.Costumes) {
		return nil, fmt.Errorf("castle: missing total war attacker score")
	}
	if len(scores) > 0 && s.totalWarSettled != nil {
		if e = s.totalWarSettled(ctx, total); e != nil {
			return nil, e
		}
	}
	if len(scores) > 0 && (len(v.TotalWarScores) == 0 || total > sum(v.TotalWarScores)) {
		if s.totalWarCompletion != nil {
			if e = s.totalWarCompletion(ctx, v.TotalWarRun.Identity, total, v.TotalWarRun.Costumes); e != nil {
				return nil, e
			}
		}
		v.TotalWarScores = scores
	}
	v.TotalWarRun = nil
	return scoresWire(v.TotalWarScores), nil
}

func (s *Service) validateSavedTotalWarBattle(ctx command.Context, v *state, blue [][]byte) error {
	if v.TotalWarDeck == nil || len(v.TotalWarDeck.Deck) == 0 {
		return fmt.Errorf("castle: total war formation not saved")
	}
	d := v.TotalWarDeck
	if e := s.validateTotalWarDeck(ctx, d.Deck); e != nil {
		return e
	}
	if e := s.validateTotalWarContents(ctx, d.Contents); e != nil {
		return e
	}
	if len(blue) != len(d.Deck) {
		return fmt.Errorf("castle: battle costumes differ from saved formation")
	}
	// The client emits attackers in formation order, then cheering costumes.
	ordered := make([][]byte, 0, len(d.Deck))
	for _, mode := range []uint64{0, 1} {
		for _, entry := range d.Deck {
			if n(entry, 1) == mode {
				ordered = append(ordered, entry)
			}
		}
	}
	contents := map[uint64][]byte{}
	for _, entry := range d.Contents {
		contents[n(entry, 1)] = entry
	}
	for i, raw := range blue {
		deck := ordered[i]
		index := n(deck, 3)
		if n(raw, 2) != index || n(raw, 6) != n(deck, 2) || n(raw, 28) != n(deck, 1) {
			return fmt.Errorf("castle: battle costume role or order differs from saved formation")
		}
		content := contents[index]
		if content == nil || n(raw, 29) != n(content, 3) {
			return fmt.Errorf("castle: battle potential differs from saved formation")
		}
		refs, e := msgs(content, 2)
		if e != nil {
			return e
		}
		want := map[uint64]bool{}
		for _, ref := range refs {
			if id := n(ref, 1); id != 0 {
				want[id] = true
			}
		}
		got := map[uint64]bool{}
		e = wire.Walk(raw, func(f wire.Field) error {
			if f.Number != 12 {
				return nil
			}
			consume := func(id uint64) error {
				if id == 0 || got[id] || !want[id] {
					return fmt.Errorf("castle: battle equipment differs from saved formation")
				}
				got[id] = true
				return nil
			}
			if f.Type == 0 {
				id, k := binary.Uvarint(f.Value)
				if k <= 0 {
					return fmt.Errorf("%w: field %d varint is truncated", wire.ErrMalformed, f.Number)
				}
				return consume(id)
			}
			if f.Type != 2 {
				return fmt.Errorf("%w: field %d has wire type %d, want varints", wire.ErrMalformed, f.Number, f.Type)
			}
			for p := f.Value; len(p) > 0; {
				id, k := binary.Uvarint(p)
				if k <= 0 {
					return fmt.Errorf("%w: field %d packed varint is truncated", wire.ErrMalformed, f.Number)
				}
				if e := consume(id); e != nil {
					return e
				}
				p = p[k:]
			}
			return nil
		})
		if e != nil {
			return e
		}
		if len(want) != len(got) {
			return fmt.Errorf("castle: battle equipment missing from saved formation")
		}
	}
	return nil
}

// AttachTotalWarSettled observes every settled attempt with its total damage.
func (s *Service) AttachTotalWarSettled(f func(command.Context, uint64) error) {
	s.totalWarSettled = f
}

func (s *Service) AttachTotalWarCompletion(f func(command.Context, string, uint64, []uint64) error) {
	s.totalWarCompletion = f
}
