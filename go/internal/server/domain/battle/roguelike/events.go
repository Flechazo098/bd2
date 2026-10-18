package roguelike

import (
	"bd2server/internal/server/protocol/wire"
	"slices"
)

func (s *Service) eventSelect(v *state, choice uint64) ([]byte, error) {
	r := v.Run
	if r.State != 5 || r.EventGroup == 0 {
		return nil, fail("no event selection")
	}
	event := s.design.Events.Row(r.EventGroup, r.EventID)
	if choice == 0 {
		if event.V(11) == 0 {
			return nil, fail("event exit unavailable")
		}
		f, rm := s.clearRoom(r)
		b := clearedResponse(r, f, rm, 1, 3, 5)
		return wire.AppendVarint(b, 2, 1), nil
	}
	if choice > uint64(len(event.A(7))) {
		return nil, fail("unknown event option")
	}
	i := choice - 1
	success := s.sample(10000) < uint64(min(1.0, float64(event.A(10)[i])/100+s.systemEffect(r, 6))*10000)
	effect := event.A(1)[i]
	if !success {
		effect = event.A(2)[i]
	}
	effects := []uint64{effect}
	if success && i < uint64(len(event.A(3))) {
		effects = append(effects, event.A(3)[i])
	}
	var reward []byte
	before := r.Gold
	for _, id := range effects {
		if id == 0 {
			continue
		}
		if r.State == 3 {
			r.DeferredEffects = append(r.DeferredEffects, id)
			continue
		}
		x, e := s.applyEffect(v, id)
		if e != nil {
			return nil, e
		}
		reward = append(reward, x...)
	}
	reward = wire.AppendVarint(reward, 1, uint64(int64(r.Gold)-int64(before)))
	r.Events++
	var f uint64
	var room *room
	if r.State == 5 {
		f, room = s.clearRoom(r)
	}
	if r.ChoiceType > 0 {
		reward = wire.AppendBytes(reward, 5, choiceWire(r))
	}
	if r.State == 3 {
		reward = wire.AppendVarint(reward, 6, r.BattleLevel)
	}
	b := clearedResponse(r, f, room, 1, 3, 5)
	result := uint64(2)
	if success {
		result = 1
	}
	b = wire.AppendVarint(b, 2, result)
	b = wire.AppendBytes(b, 4, reward)
	return wire.AppendVarint(b, 6, r.Reroll), nil
}
func (s *Service) applyEffect(v *state, id uint64) ([]byte, error) {
	r := v.Run
	effect := s.design.Effects.Row(0, id)
	if effect.Values == nil {
		return nil, fail("missing event effect")
	}
	a, z := effect.V(2), effect.V(3)
	var b []byte
	switch effect.V(1) {
	case 1:
		r.State = 3
		r.BattleLevel = a
	case 2:
		if z < a {
			return nil, fail("invalid gold range")
		}
		r.Gold += a + s.sample(z-a+1)
	case 3:
		if r.Gold < a {
			return nil, fail("insufficient event gold")
		}
		r.Gold -= a
	case 4, 5:
		sign := int64(1)
		if effect.V(1) == 5 {
			sign = -1
		}
		for i := range r.Characters {
			c := &r.Characters[i]
			delta := int64(c.MaxHP*a/100) * sign
			hp := int64(c.HP) + delta
			hp = max(hp, 0)
			hp = min(hp, int64(c.MaxHP))
			c.HP = uint64(hp)
			b = wire.AppendBytes(b, 2, charWire(*c, s.design.Default.V(7)))
		}
		b = wire.AppendVarint(b, 7, uint64(int64(a)*sign))
	case 6:
		item, e := s.addRelic(v, a)
		if e != nil {
			return nil, e
		}
		b = wire.AppendBytes(b, 3, relicWire(item))
	case 7:
		itemID, e := s.rollRelic(r, s.design.RelicChoices.Row(0, a), nil)
		if e != nil {
			return nil, e
		}
		item, e := s.addRelic(v, itemID)
		if e != nil {
			return nil, e
		}
		b = wire.AppendBytes(b, 3, relicWire(item))
	case 8:
		r.State = 2
		r.Remaining = 1
		if e := s.newChoice(r, 2, a, false); e != nil {
			return nil, e
		}
	case 9:
		if len(r.Relics) == 0 {
			return nil, fail("no relic to exchange")
		}
		for _, item := range r.Relics {
			if item.ID == a {
				return nil, fail("exchange result owned")
			}
		}
		r.State = 6
		r.PendingRelic = a
	case 10:
		if len(r.Relics) == 0 {
			return nil, fail("no relic to exchange")
		}
		index := s.sample(uint64(len(r.Relics)))
		old := r.Relics[index]
		newID, e := s.rollRelic(r, s.design.RelicChoices.Row(0, a), nil)
		if e != nil {
			return nil, e
		}
		r.Relics = append(r.Relics[:index], r.Relics[index+1:]...)
		add, e := s.addRelic(v, newID)
		if e != nil {
			return nil, e
		}
		b = wire.AppendBytes(b, 4, relicWire(old))
		b = wire.AppendBytes(b, 3, relicWire(add))
	case 11:
		if len(r.Relics) == 0 {
			return nil, fail("no relic to remove")
		}
		r.State = 7
	case 12:
		if len(r.Relics) == 0 {
			return nil, fail("no relic to remove")
		}
		i := s.sample(uint64(len(r.Relics)))
		b = wire.AppendBytes(b, 4, relicWire(r.Relics[i]))
		r.Relics = append(r.Relics[:i], r.Relics[i+1:]...)
	default:
		return nil, fail("unsupported event effect")
	}
	return b, nil
}
func (s *Service) eventChoice(v *state, typ, id uint64) ([]byte, error) {
	r := v.Run
	if typ != 2 || r.State != 6 && r.State != 7 {
		return nil, fail("no pending relic selection")
	}
	i := slices.IndexFunc(r.Relics, func(a relic) bool { return a.ID == id })
	if i < 0 {
		return nil, fail("selected relic unowned")
	}
	old := r.Relics[i]
	r.Relics = slices.Delete(r.Relics, i, i+1)
	reward := wire.AppendBytes(nil, 4, relicWire(old))
	if r.State == 6 {
		add, e := s.addRelic(v, r.PendingRelic)
		if e != nil {
			return nil, e
		}
		reward = wire.AppendBytes(reward, 3, relicWire(add))
	}
	r.PendingRelic = 0
	f, rm := s.clearRoom(r)
	out := clearedResponse(r, f, rm, 1, 3, 5)
	out = wire.AppendVarint(out, 2, 1)
	out = wire.AppendBytes(out, 4, reward)
	return wire.AppendVarint(out, 6, r.Reroll), nil
}
