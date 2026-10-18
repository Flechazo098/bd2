package roguelike

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
)

func (s *Service) HandlesBattle(mode uint64) bool { return mode == 21 || mode == 27 }
func (s *Service) battleRule(r *run) (uint64, uint64) {
	if r.State == 9 {
		rule := s.design.Rooms.Row(s.design.Default.V(19), s.design.Default.V(20))
		return rule.V(1), 27
	}
	_, rule := s.current(r)
	return rule.V(1), 21
}
func (s *Service) EnterBattle(ctx command.Context, b []byte, key string) ([]byte, error) {
	v, p, e := s.load(ctx)
	if e != nil {
		return nil, e
	}
	r := v.Run
	if !p.Active || r == nil || r.State != 3 && r.State != 9 || r.Encounter != "" {
		return nil, fail("no eligible battle room")
	}
	deck, mode := s.battleRule(r)
	if n(b, 4) != deck || n(b, 5) != mode || n(b, 3) != 0 {
		return nil, fail("battle does not match selected room")
	}
	if len(r.Deck) == 0 {
		return nil, fail("formation not saved")
	}
	r.ChangedParticipants = nil
	r.Encounter = key
	r.EncounterMode = mode
	return nil, s.save(ctx, v)
}
func (s *Service) ValidateBattleStart(ctx command.Context, b []byte) error {
	v, _, e := s.load(ctx)
	if e != nil {
		return e
	}
	r := v.Run
	if r == nil || r.Encounter == "" || s.options.Evidence == nil {
		return fail("no encounter")
	}
	proof := s.options.Evidence(ctx)
	if proof.Receipt != r.Encounter {
		return fail("battle receipt mismatch")
	}
	if proof.Round > 0 {
		return nil
	}
	blue, e := messages(b, 5)
	if e != nil {
		return e
	}
	expected := map[uint64][]byte{}
	for _, d := range r.Deck {
		if int64(n(d, 2)) >= 0 {
			expected[n(d, 1)] = d
		}
	}
	if len(blue) != len(expected) || len(blue) == 0 || len(blue) > 5 {
		return fail("battle party differs from formation")
	}
	seen := map[uint64]bool{}
	unique := map[uint64]bool{}
	for _, a := range blue {
		index := n(a, 2)
		d, ok := expected[index]
		if !ok || seen[index] || n(a, 1) == 0 || unique[n(a, 1)] {
			return fail("invalid battle character")
		}
		seen[index] = true
		unique[n(a, 1)] = true
		var c *character
		for i := range r.Characters {
			if r.Characters[i].Index == index {
				c = &r.Characters[i]
			}
		}
		if c == nil || c.HP == 0 || n(a, 3) != c.ID || n(a, 4) != c.HP || n(a, 5) != s.design.Default.V(7) || n(a, 9) != n(d, 2) {
			return fail("battle character differs from run")
		}
		valid := false
		for _, costume := range r.Costumes {
			if costume.Character == c.Index && n(a, 6) == costume.Index && n(a, 7) == costume.ID && n(a, 8) == costume.Level {
				valid = true
			}
		}
		if !valid {
			return fail("battle costume unowned")
		}
		if n(a, 12) != 0 {
			return fail("roguelike equipment unavailable")
		}
	}
	deck, _ := s.battleRule(r)
	red, e := messages(b, 4)
	if e != nil {
		return e
	}
	want := s.design.Enemies[deck]
	if len(red) != len(want) {
		return fail("enemy party differs from GameData")
	}
	for i, a := range red {
		enemy := want[i]
		if n(a, 1) == 0 || unique[n(a, 1)] || n(a, 2) != 0 || n(a, 3) != enemy.ID || n(a, 5) != enemy.Level || n(a, 9) != enemy.Position || n(a, 4) == 0 {
			return fail("invalid enemy party")
		}
		unique[n(a, 1)] = true
	}
	return nil
}
func (s *Service) preview(ctx command.Context, v *state, b []byte) ([]byte, error) {
	r := v.Run
	if r.Encounter == "" || s.options.Evidence == nil {
		return nil, fail("preview without battle")
	}
	proof := s.options.Evidence(ctx)
	result, damage := n(b, 2), n(b, 3)
	if proof.Receipt != r.Encounter || proof.Round == 0 || result == 0 || result > 4 || damage > math.MaxInt64 {
		return nil, fail("invalid battle preview")
	}
	out := wire.AppendVarint(nil, 1, result)
	if r.State == 9 {
		out = wire.AppendVarint(out, 4, damage)
	} else if result == 1 {
		_, room := s.current(r)
		out = wire.AppendVarint(out, 3, uint64(float64(room.V(3))*s.design.Levels.Row(0, r.Level).D(5)))
	}
	return out, nil
}
func (s *Service) CompleteBattle(ctx command.Context, b []byte, key string) ([]byte, error) {
	v, p, e := s.load(ctx)
	if e != nil {
		return nil, e
	}
	r := v.Run
	if !p.Active || r == nil || r.Encounter != key || s.options.Evidence == nil {
		return nil, fail("settlement without encounter")
	}
	proof := s.options.Evidence(ctx)
	deck, mode := s.battleRule(r)
	if proof.Receipt != key || proof.Mode != mode || proof.Deck != deck || proof.Round == 0 || len(proof.InitialBlue) == 0 {
		return nil, fail("settlement before started battle")
	}
	result := n(b, 2)
	if result == 0 || result > 4 {
		return nil, fail("invalid result")
	}
	chars, e := messages(b, 3)
	if e != nil {
		return nil, e
	}
	participants := map[uint64]bool{}
	for _, a := range proof.InitialBlue {
		participants[n(a, 2)] = true
	}
	for _, index := range r.ChangedParticipants {
		participants[index] = true
	}
	seen := map[uint64]bool{}
	var out []byte
	for _, raw := range chars {
		index := n(raw, 1)
		if !participants[index] || seen[index] {
			return nil, fail("unknown or repeated settlement character")
		}
		seen[index] = true
		found := false
		for i := range r.Characters {
			c := &r.Characters[i]
			if c.Index != index {
				continue
			}
			if n(raw, 2) != c.ID || n(raw, 3) > c.MaxHP {
				return nil, fail("invalid settlement health")
			}
			c.HP = n(raw, 3)
			found = true
		}
		if !found {
			return nil, fail("settlement character unowned")
		}
	}
	if len(chars) != len(participants) {
		return nil, fail("missing settlement characters")
	}
	r.Encounter = ""
	var score, bundle []byte
	var f uint64
	var cleared *room
	if mode == 27 {
		damage := n(b, 9)
		if damage > math.MaxInt64 {
			return nil, fail("crystal damage overflow")
		}
		r.Crystal = damage
		score, bundle, e = s.finish(ctx, v, true, false, key+":crystal")
	} else if result != 1 {
		score, bundle, e = s.finish(ctx, v, false, false, key+":defeat")
	} else {
		var eventReward []byte
		beforeGold := r.Gold
		for _, effect := range r.DeferredEffects {
			x, err := s.applyEffect(v, effect)
			if err != nil {
				return nil, err
			}
			eventReward = append(eventReward, x...)
		}
		r.DeferredEffects = nil
		if r.Gold != beforeGold {
			eventReward = wire.AppendVarint(eventReward, 1, r.Gold-beforeGold)
		}
		if len(eventReward) > 0 {
			out = wire.AppendBytes(out, 20, eventReward)
		}
		_, room := s.current(r)
		gold := uint64(float64(room.V(3)) * s.design.Levels.Row(0, r.Level).D(5) * (1 + s.systemEffect(r, 1) + s.systemEffect(r, bonusGoldType(room.V(10)))))
		r.Gold += gold
		out = wire.AppendVarint(out, 21, gold)
		switch room.V(10) {
		case 2:
			r.Elites++
		case 3:
			r.Bosses++
		}
		if r.Floor == uint64(len(r.Floors)) {
			f, cleared = s.clearRoom(r)
			if r.Level >= s.design.Default.V(14) && f >= s.design.Default.V(13) {
				r.State = 9
				r.Room = 0
				r.BattleLevel = s.design.Default.V(17)
			} else {
				score, bundle, e = s.finish(ctx, v, true, false, fmt.Sprintf("%s:clear", key))
			}
		} else if r.State == 2 || r.State == 6 || r.State == 7 {
		} else if room.V(4) > 0 {
			r.State = 1
			r.Remaining = room.V(4)
			e = s.newChoice(r, 1, 0, false)
		} else {
			f, cleared = s.clearRoom(r)
		}
	}
	if e != nil {
		return nil, e
	}
	out = wire.AppendBytes(out, 17, stateWire(v.Run))
	if v.Run != nil {
		out = deckWire(out, 30, v.Run)
		out = wire.AppendBytes(out, 18, choiceWire(v.Run))
		out = wire.AppendVarint(out, 19, v.Run.Reroll)
		for _, c := range v.Run.Characters {
			out = wire.AppendBytes(out, 3, charWire(c, s.design.Default.V(7)))
		}
	}
	if score != nil {
		out = wire.AppendBytes(out, 22, score)
	}
	if bundle != nil {
		out = wire.AppendBytes(out, 5, bundle)
	}
	if f > 0 && cleared != nil {
		out = wire.AppendVarint(out, 23, f)
		out = wire.AppendBytes(out, 24, roomWire(*cleared))
	}
	if e = s.save(ctx, v); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *Service) AbandonBattle(ctx command.Context) error {
	v, _, e := s.load(ctx)
	if e != nil {
		return e
	}
	if v.Run != nil {
		v.Run.Encounter = ""
	}
	return s.save(ctx, v)
}
