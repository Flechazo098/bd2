package roguelike

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

func (s *Service) shopPrice(r *run) uint64 {
	_, rule := s.current(r)
	shop := s.design.Shops.Row(0, rule.V(9))
	return shop.V(5) + r.ShopRoll*shop.V(3)
}
func (s *Service) newShop(v *state) error {
	r := v.Run
	_, rule := s.current(r)
	d := s.design.Shops.Row(0, rule.V(9))
	if d.Values == nil {
		return fail("missing shop")
	}
	r.Shop = nil
	pool := s.costumePool(r, false)
	for _, option := range d.A(1) {
		id, remaining, ok := s.pickCostume(r, pool, option, false)
		if !ok {
			break
		}
		r.Shop = append(r.Shop, offer{Type: 1, ID: id, Price: s.design.Default.V(36)})
		pool = remaining
	}
	var selected []uint64
	for i := uint64(0); i < d.V(7); i++ {
		id, e := s.rollRelic(r, s.design.RelicChoices.Row(0, d.V(6)), selected)
		if e != nil {
			break
		}
		selected = append(selected, id)
		r.Shop = append(r.Shop, offer{Type: 2, ID: id, Price: s.design.Relics.Row(0, id).V(8)})
	}
	r.Shop = append(r.Shop, offer{Type: 3, ID: 0, Price: s.design.Default.V(38)})
	discount := s.design.Levels.Row(0, r.Level).D(12) + s.design.Growth.Row(6, v.Growth[6]).D(6)
	for i := range r.Shop {
		if s.sample(10000) < uint64(discount*10000) {
			r.Shop[i].Price = uint64(float64(r.Shop[i].Price) * (1 - s.design.Default.D(37)))
		}
	}
	return nil
}
func (s *Service) shopBuy(ctx command.Context, v *state, typ, id uint64) ([]byte, error) {
	r := v.Run
	beforeGold := r.Gold
	if r.State != 4 {
		return nil, fail("not in shop")
	}
	var selected *offer
	for i := range r.Shop {
		o := &r.Shop[i]
		if o.Type == typ && o.ID == id && !o.Sold {
			selected = o
			break
		}
	}
	if selected == nil || selected.Price > r.Gold {
		return nil, fail("offer unavailable or insufficient gold")
	}
	var b []byte
	switch typ {
	case 1:
		c, a, e := s.addCostume(ctx, v, id)
		if e != nil {
			return nil, e
		}
		if c != nil {
			b = wire.AppendBytes(b, 1, charWire(*c, s.design.Default.V(7)))
		}
		b = wire.AppendBytes(b, 2, costumeWire(*a))
	case 2:
		a, e := s.addRelic(v, id)
		if e != nil {
			return nil, e
		}
		b = wire.AppendBytes(b, 3, relicWire(a))
	case 3:
		changed := false
		for i := range r.Characters {
			c := &r.Characters[i]
			if c.HP < c.MaxHP {
				c.HP = min(c.MaxHP, c.HP+c.MaxHP*s.design.Default.V(39)/100)
				changed = true
				b = wire.AppendBytes(b, 1, charWire(*c, s.design.Default.V(7)))
			}
		}
		if !changed {
			return nil, fail("no character needs healing")
		}
	default:
		return nil, fail("unknown shop item")
	}
	r.Gold -= selected.Price
	selected.Sold = true
	b = wire.AppendVarint(b, 4, uint64(int64(r.Gold)-int64(beforeGold)))
	return wire.AppendVarint(b, 5, r.Reroll), nil
}
func (s *Service) shopReroll(v *state) ([]byte, error) {
	r := v.Run
	if r.State != 4 {
		return nil, fail("not in shop")
	}
	price := s.shopPrice(r)
	if r.Gold < price {
		return nil, fail("insufficient shop gold")
	}
	r.Gold -= price
	r.ShopRoll++
	if e := s.newShop(v); e != nil {
		return nil, e
	}
	b := wire.AppendBytes(nil, 1, shopWire(r, s.shopPrice(r)))
	return wire.AppendVarint(b, 2, uint64(-int64(price))), nil
}
func (s *Service) upgrade(r *run, index, count uint64, burst bool) ([]byte, error) {
	if r.State != 4 || count == 0 || count > math.MaxInt32 {
		return nil, fail("invalid shop upgrade")
	}
	for i := range r.Costumes {
		c := &r.Costumes[i]
		if c.Index != index {
			continue
		}
		d := s.design.Costumes[c.ID]
		current, maximum, price := c.Level, d.MaxLevel, s.design.Default.V(12)
		if burst {
			current, maximum, price = c.Burst, d.MaxBurst, s.design.Default.V(3)
		}
		if current > maximum || count > maximum-current || count > math.MaxInt32/max(price, 1) || r.Gold < count*price {
			return nil, fail("upgrade exceeds limit or gold")
		}
		cost := count * price
		r.Gold -= cost
		if burst {
			c.Burst += count
		} else {
			c.Level += count
		}
		return wire.AppendVarint(wire.AppendBytes(nil, 1, costumeWire(*c)), 2, uint64(-int64(cost))), nil
	}
	return nil, fail("costume not owned")
}
func (s *Service) deckSave(r *run, b []byte) ([]byte, error) {
	if r.State == 3 || r.State == 9 || r.Encounter != "" {
		return nil, fail("formation locked in battle")
	}
	entries, e := messages(b, 2)
	if e != nil {
		return nil, e
	}
	if len(entries) == 0 || len(entries) > len(r.Characters) {
		return nil, fail("invalid formation size")
	}
	seen, positions, sequences := map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}
	var active uint64
	for _, raw := range entries {
		id, pos, seq := n(raw, 1), int64(n(raw, 2)), n(raw, 3)
		var c *character
		for i := range r.Characters {
			if r.Characters[i].Index == id {
				c = &r.Characters[i]
			}
		}
		if c == nil || seen[id] || pos < -1 || pos > 11 || seq == 0 || seq > uint64(len(r.Characters)) {
			return nil, fail("invalid formation member")
		}
		seen[id] = true
		if pos >= 0 {
			if c.HP == 0 || positions[uint64(pos)] || sequences[seq] {
				return nil, fail("invalid active formation")
			}
			positions[uint64(pos)] = true
			sequences[seq] = true
			active++
		}
	}
	if active == 0 || active > uint64(max(1, 5-int(s.systemEffect(r, 9)))) {
		return nil, fail("invalid active party")
	}
	r.Deck = entries
	return deckWire(nil, 1, r), nil
}
func (s *Service) change(ctx command.Context, r *run, in, out uint64) ([]byte, error) {
	if r.Encounter == "" || r.State != 3 && r.State != 9 || in == out || in == 0 && out == 0 || s.options.Evidence == nil {
		return nil, fail("no active battle change")
	}
	proof := s.options.Evidence(ctx)
	if proof.Receipt != r.Encounter || proof.Round == 0 {
		return nil, fail("change before battle start")
	}
	incoming, outgoing := -1, -1
	active := 0
	for i, d := range r.Deck {
		if n(d, 1) == in {
			incoming = i
		}
		if n(d, 1) == out {
			outgoing = i
		}
		if int64(n(d, 2)) >= 0 {
			active++
		}
	}
	if out != 0 && (outgoing < 0 || int64(n(r.Deck[outgoing], 2)) < 0) {
		return nil, fail("outgoing character not deployed")
	}
	if in != 0 {
		alive := false
		for _, c := range r.Characters {
			if c.Index == in && c.HP > 0 {
				alive = true
			}
		}
		if !alive || incoming >= 0 && int64(n(r.Deck[incoming], 2)) >= 0 {
			return nil, fail("incoming character not an alive reserve")
		}
		if out == 0 && active >= max(1, 5-int(s.systemEffect(r, 9))) {
			return nil, fail("active party is full")
		}
	}
	position, sequence := uint64(0), uint64(active+1)
	if outgoing >= 0 {
		position, sequence = n(r.Deck[outgoing], 2), n(r.Deck[outgoing], 3)
	} else {
		used := map[uint64]bool{}
		for _, d := range r.Deck {
			if int64(n(d, 2)) >= 0 {
				used[n(d, 2)] = true
			}
		}
		for used[position] {
			position++
		}
	}
	if outgoing >= 0 {
		r.Deck[outgoing] = wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, out), 2, math.MaxUint64), 3, n(r.Deck[outgoing], 3))
	}
	if in != 0 {
		entry := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, in), 2, position), 3, sequence)
		if incoming < 0 {
			r.Deck = append(r.Deck, entry)
		} else {
			r.Deck[incoming] = entry
		}
		if !slices.Contains(r.ChangedParticipants, in) {
			r.ChangedParticipants = append(r.ChangedParticipants, in)
		}
	}
	if out != 0 && !slices.Contains(r.ChangedParticipants, out) {
		r.ChangedParticipants = append(r.ChangedParticipants, out)
	}
	return nil, nil
}
func (s *Service) mix(v *state, b []byte) ([]byte, error) {
	r := v.Run
	if r.State == 3 || r.State == 9 {
		return nil, fail("cannot mix in battle")
	}
	var indexes []uint64
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		switch f.Type {
		case 0:
			x, k := binary.Uvarint(f.Value)
			if k <= 0 {
				return fmt.Errorf("%w: field %d varint is truncated", wire.ErrMalformed, f.Number)
			}
			indexes = append(indexes, x)
		case 2:
			for p := f.Value; len(p) > 0; {
				x, k := binary.Uvarint(p)
				if k <= 0 {
					return fmt.Errorf("%w: field %d packed varint is truncated", wire.ErrMalformed, f.Number)
				}
				indexes = append(indexes, x)
				p = p[k:]
			}
		default:
			return fmt.Errorf("%w: field %d has wire type %d, want varints", wire.ErrMalformed, f.Number, f.Type)
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	if len(indexes) != 2 || indexes[0] == indexes[1] {
		return nil, fail("mix requires two distinct relics")
	}
	var materials []relic
	for _, a := range r.Relics {
		if slices.Contains(indexes, a.Index) {
			materials = append(materials, a)
		}
	}
	if len(materials) != 2 {
		return nil, fail("mix material unowned")
	}
	var result uint64
	for _, id := range s.design.Mix.Groups[0] {
		rule := s.design.Mix.Row(0, id)
		a, b := rule.V(2), rule.V(3)
		if materials[0].ID == a && materials[1].ID == b || materials[1].ID == a && materials[0].ID == b {
			result = rule.V(4)
			break
		}
	}
	if result == 0 {
		return nil, fail("no matching relic recipe")
	}
	add, e := s.addRelic(v, result)
	if e != nil {
		return nil, e
	}
	r.Relics = slices.DeleteFunc(r.Relics, func(a relic) bool { return slices.Contains(indexes, a.Index) })
	out := wire.AppendBytes(nil, 1, relicWire(add))
	for _, a := range materials {
		out = wire.AppendBytes(out, 2, relicWire(a))
	}
	return wire.AppendVarint(out, 3, r.Reroll), nil
}
