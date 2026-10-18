package roguelike

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"math"
	"slices"
)

func (s *Service) total(ctx command.Context, v *state) (uint64, error) {
	if s.options.Ranking != nil {
		if pending, ok := s.options.Ranking.(interface {
			PendingTotal(command.Context, uint64, RankEntry) (uint64, error)
		}); ok {
			return pending.PendingTotal(ctx, v.Season, RankEntry{Owner: s.options.Owner, Score: v.Seasons[v.Season].Score})
		}
		return s.options.Ranking.Total(ctx, v.Season)
	}
	return v.Seasons[v.Season].Score, nil
}
func (s *Service) info(ctx command.Context, v *state, p Period) ([]byte, error) {
	b := wire.AppendBytes(nil, 1, stateWire(v.Run))
	if r := v.Run; r != nil {
		b = wire.AppendVarint(b, 2, r.Level)
		for _, f := range r.Floors {
			b = wire.AppendBytes(b, 3, floorWire(f))
		}
		b = deckWire(b, 4, r)
		for _, c := range r.Characters {
			b = wire.AppendBytes(b, 5, charWire(c, s.design.Default.V(7)))
		}
		for _, c := range r.Costumes {
			b = wire.AppendBytes(b, 6, costumeWire(c))
		}
		for _, a := range r.Relics {
			b = wire.AppendBytes(b, 7, relicWire(a))
		}
		b = wire.AppendBytes(b, 8, choiceWire(r))
		b = wire.AppendVarint(b, 9, r.Reroll)
		b = wire.AppendBytes(b, 13, eventWire(r))
		b = wire.AppendBytes(b, 14, shopWire(r, s.shopPrice(r)))
		b = wire.AppendVarint(b, 15, r.BattleLevel)
		b = wire.AppendVarint(b, 18, r.Gold)
	}
	keys := make([]uint64, 0, len(v.Growth))
	for typ := range v.Growth {
		keys = append(keys, typ)
	}
	slices.Sort(keys)
	for _, typ := range keys {
		x := wire.AppendVarint(nil, 1, typ)
		x = wire.AppendVarint(x, 2, v.Growth[typ])
		b = wire.AppendBytes(b, 12, x)
	}
	b = wire.AppendVarint(b, 16, v.MaxTry)
	b = wire.AppendVarint(b, 17, v.Obsidian)
	b = wire.AppendVarint(b, 19, v.Season)
	b = wire.AppendVarint(b, 20, p.Regular)
	for season, r := range v.Seasons {
		if s.seasonSettled(season, v.Season) && r.Score > 0 && !r.Claimed {
			b = wire.AppendVarint(b, 21, 1)
			break
		}
	}
	b = wire.AppendVarint(b, 22, v.MaxReward)
	return wire.AppendVarint(b, 23, v.HighestCrystal), nil
}
func (s *Service) enter(ctx command.Context, v *state, p Period, level uint64, key string) ([]byte, error) {
	l := s.design.Levels.Row(0, level)
	if !p.Active || v.Run != nil || l.Values == nil || level > v.MaxTry+1 {
		return nil, fail("difficulty unavailable")
	}
	r := &run{ID: v.NextRun, Level: level, Floor: 1, State: 8, Gold: l.V(15), Reroll: s.design.Default.V(44), Remaining: s.design.Default.V(42), ChoiceType: 1}
	v.NextRun++
	growth := s.design.Growth.Row(8, v.Growth[8])
	r.Reroll += uint64(growth.D(6))
	for f := uint64(1); f <= l.V(4); f++ {
		rule := s.design.Floors.Row(level, f)
		if rule.Values == nil {
			return nil, fail("missing floor template")
		}
		count := uint64(3)
		if f%2 == 0 {
			count = 4
		}
		if slices.Contains(l.A(1), f) {
			count = 1
		}
		fl := floor{Number: f}
		for num := uint64(1); num <= count; num++ {
			group, e := s.weighted(rule.A(11), rule.A(12))
			if e != nil {
				return nil, e
			}
			ids := s.design.Rooms.Groups[group]
			if len(ids) == 0 {
				return nil, fail("empty room group")
			}
			id := ids[s.sample(uint64(len(ids)))]
			fl.Rooms = append(fl.Rooms, room{Number: num, Group: group, ID: id})
		}
		for _, required := range []struct{ typ, count uint64 }{{6, rule.V(13)}, {4, rule.V(14)}} {
			var group uint64
			for _, g := range rule.A(11) {
				ids := s.design.Rooms.Groups[g]
				if len(ids) > 0 && s.design.Rooms.Row(g, ids[0]).V(10) == required.typ {
					group = g
					break
				}
			}
			if required.count > 0 && group == 0 {
				return nil, fail("required room pool unavailable")
			}
			for i := uint64(0); i < required.count; i++ {
				at := uint64(len(fl.Rooms)) - 1 - i
				if i >= uint64(len(fl.Rooms)) {
					return nil, fail("required rooms exceed map")
				}
				ids := s.design.Rooms.Groups[group]
				fl.Rooms[at].Group = group
				fl.Rooms[at].ID = ids[s.sample(uint64(len(ids)))]
			}
		}
		r.Floors = append(r.Floors, fl)
	}
	if e := s.newChoice(r, 1, 0, true); e != nil {
		return nil, e
	}
	if e := s.options.Tickets(ctx, key, s.design.Default.V(30)); e != nil {
		return nil, e
	}
	v.Run = r
	var b []byte
	for _, f := range r.Floors {
		b = wire.AppendBytes(b, 1, floorWire(f))
	}
	b = wire.AppendBytes(b, 2, choiceWire(r))
	b = wire.AppendBytes(b, 3, stateWire(r))
	b = wire.AppendVarint(b, 4, r.Gold)
	return wire.AppendVarint(b, 5, r.Reroll), nil
}
func (s *Service) weighted(ids, weights []uint64) (uint64, error) {
	if len(ids) == 0 || len(ids) != len(weights) {
		return 0, fail("invalid weighted pool")
	}
	var total uint64
	for _, w := range weights {
		if total > math.MaxUint64-w {
			return 0, fail("weight overflow")
		}
		total += w
	}
	if total == 0 {
		return 0, fail("empty weighted pool")
	}
	roll := s.sample(total)
	for i, w := range weights {
		if roll < w {
			return ids[i], nil
		}
		roll -= w
	}
	return 0, fail("invalid random sample")
}
func (s *Service) current(r *run) (*room, gamedata.RogueRule) {
	if r.Floor == 0 || r.Floor > uint64(len(r.Floors)) {
		return nil, gamedata.RogueRule{}
	}
	for i := range r.Floors[r.Floor-1].Rooms {
		x := &r.Floors[r.Floor-1].Rooms[i]
		if x.Number == r.Room {
			return x, s.design.Rooms.Row(x.Group, x.ID)
		}
	}
	return nil, gamedata.RogueRule{}
}
func (s *Service) roomEnter(v *state, number uint64) ([]byte, error) {
	r := v.Run
	if r.State != 0 || r.Floor == 0 || r.Floor > uint64(len(r.Floors)) {
		return nil, fail("room unavailable")
	}
	var found *room
	for i := range r.Floors[r.Floor-1].Rooms {
		x := &r.Floors[r.Floor-1].Rooms[i]
		if x.Number == number && !x.Clear && x.Group > 0 {
			found = x
			break
		}
	}
	if found == nil {
		return nil, fail("unknown room")
	}
	bosses := s.design.Default.A(2)
	connected := r.Floor == 1 || slices.Contains(bosses, r.Floor) || slices.Contains(bosses, r.Floor-1)
	if !connected {
		for _, prev := range r.Floors[r.Floor-2].Rooms {
			if prev.Clear && (prev.Number == number || (r.Floor%2 == 1 && prev.Number+1 == number) || (r.Floor%2 == 0 && prev.Number == number+1)) {
				connected = true
			}
		}
	}
	if !connected {
		return nil, fail("room is disconnected")
	}
	gain := uint64(float64(r.Gold)*(s.design.Default.D(27)+s.systemEffect(r, 2))) + uint64(s.systemEffect(r, 7))
	if r.Floor == 1 {
		gain = 0
	}
	r.Gold += gain
	r.Room = number
	r.EventGroup = 0
	r.EventID = 0
	r.Choice = nil
	r.ChoiceType = 0
	r.Shop = nil
	r.ShopRoll = 0
	rule := s.design.Rooms.Row(found.Group, found.ID)
	fl := s.design.Floors.Row(r.Level, r.Floor)
	switch rule.V(10) {
	case 1, 2, 3:
		r.State = 3
		min, max := 10, 9
		if rule.V(10) == 2 {
			min, max = 4, 3
		}
		if rule.V(10) == 3 {
			min, max = 2, 1
		}
		r.BattleLevel = fl.V(min) + s.sample(fl.V(max)-fl.V(min)+1)
	case 4:
		r.State = 2
		r.Remaining = 1
		if e := s.newChoice(r, 2, rule.V(8), false); e != nil {
			return nil, e
		}
	case 5:
		r.State = 5
		groups := s.design.EventChoices.Row(0, rule.V(5)).A(1)
		if len(groups) == 0 {
			return nil, fail("missing event choices")
		}
		r.EventGroup = groups[s.sample(uint64(len(groups)))]
		ids := s.design.Events.Groups[r.EventGroup]
		if len(ids) == 0 {
			return nil, fail("empty event group")
		}
		r.EventID = ids[s.sample(uint64(len(ids)))]
		r.BattleLevel = fl.V(6) + s.sample(fl.V(5)-fl.V(6)+1)
	case 6:
		r.State = 4
		if e := s.newShop(v); e != nil {
			return nil, e
		}
	default:
		return nil, fail("invalid room type")
	}
	b := wire.AppendBytes(nil, 1, stateWire(r))
	b = wire.AppendBytes(b, 2, choiceWire(r))
	if r.State == 5 {
		b = wire.AppendBytes(b, 3, eventWire(r))
	}
	if r.State == 4 {
		b = wire.AppendBytes(b, 4, shopWire(r, s.shopPrice(r)))
	}
	b = wire.AppendVarint(b, 6, gain)
	return wire.AppendVarint(b, 5, r.BattleLevel), nil
}
func (s *Service) clearRoom(r *run) (uint64, *room) {
	f := r.Floor
	rm, _ := s.current(r)
	var result *room
	if rm != nil {
		rm.Clear = true
		x := *rm
		result = &x
		r.Cleared++
	}
	r.Floor++
	r.State = 0
	r.ChoiceType = 0
	r.Choice = nil
	r.Remaining = 0
	r.Shop = nil
	r.EventGroup = 0
	r.EventID = 0
	return f, result
}
func (s *Service) newChoice(r *run, typ, choice uint64, wild bool) error {
	r.ChoiceType = typ
	r.RelicChoice = choice
	r.Choice = nil
	if typ == 1 {
		initial := wild || r.Room == 0
		pool := s.costumePool(r, initial)
		if wild {
			r.Choice = pool
			return nil
		}
		for _, field := range []int{8, 9, 10, 11} {
			id, remaining, ok := s.pickCostume(r, pool, s.design.Default.V(field), initial)
			if !ok {
				break
			}
			r.Choice = append(r.Choice, id)
			pool = remaining
		}
	} else if typ == 2 {
		rule := s.design.RelicChoices.Row(0, choice)
		if rule.Values == nil {
			return fail("missing relic pool")
		}
		for len(r.Choice) < 3 {
			id, e := s.rollRelic(r, rule, r.Choice)
			if e != nil {
				break
			}
			r.Choice = append(r.Choice, id)
		}
	}
	if len(r.Choice) == 0 {
		return fail("no eligible choice")
	}
	return nil
}
func (s *Service) rollRelic(r *run, choice gamedata.RogueRule, exclude []uint64) (uint64, error) {
	var grades, weights []uint64
	for i, w := range choice.A(2) {
		if w > 0 {
			grades = append(grades, uint64(i))
			weights = append(weights, w)
		}
	}
	grade, e := s.weighted(grades, weights)
	if e != nil {
		return 0, e
	}
	var ids []uint64
	for _, id := range s.design.Relics.Groups[0] {
		a := s.design.Relics.Row(0, id)
		if a.V(1) != grade || choice.V(3) != 0 && a.V(9) != choice.V(3) || slices.Contains(exclude, id) {
			continue
		}
		owned := false
		for _, item := range r.Relics {
			if item.ID == id {
				owned = true
			}
		}
		if !owned {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return 0, fail("relic pool exhausted")
	}
	return ids[s.sample(uint64(len(ids)))], nil
}
func (s *Service) addCostume(ctx command.Context, v *state, id uint64) (*character, *costume, error) {
	r := v.Run
	d, ok := s.design.Costumes[id]
	if !ok {
		return nil, nil, fail("invalid costume")
	}
	for i := range r.Costumes {
		c := &r.Costumes[i]
		if c.ID == id {
			if c.Level >= d.MaxLevel {
				return nil, nil, fail("costume at maximum")
			}
			c.Level++
			return nil, c, nil
		}
	}
	var char *character
	for i := range r.Characters {
		if r.Characters[i].Unique == d.Unique {
			char = &r.Characters[i]
			break
		}
	}
	if char == nil {
		hp := d.Health
		growth := s.design.Growth.Row(1, v.Growth[1])
		hp = uint64(float64(hp) * (1 + growth.D(6)))
		if s.options.Health != nil {
			var e error
			hp, e = s.options.Health(ctx, d, growth.D(6))
			if e != nil {
				return nil, nil, e
			}
		}
		r.Characters = append(r.Characters, character{Index: s.index(v), ID: d.Character, Unique: d.Unique, HP: hp, MaxHP: hp})
		char = &r.Characters[len(r.Characters)-1]
	}
	c := costume{Index: s.index(v), ID: id, Character: char.Index}
	r.Costumes = append(r.Costumes, c)
	if char.Costume == 0 {
		char.Costume = c.Index
	}
	return char, &r.Costumes[len(r.Costumes)-1], nil
}
func (s *Service) addRelic(v *state, id uint64) (relic, error) {
	if s.design.Relics.Row(0, id).Values == nil {
		return relic{}, fail("unknown relic")
	}
	for _, r := range v.Run.Relics {
		if r.ID == id {
			return relic{}, fail("relic already owned")
		}
	}
	r := relic{s.index(v), id}
	v.Run.Relics = append(v.Run.Relics, r)
	for _, buffID := range s.design.Relics.Row(0, id).A(3) {
		buff := s.design.Buffs.Row(0, buffID)
		if buff.V(6) == 8 {
			v.Run.Gold += uint64(buff.D(7))
		}
		if buff.V(6) == 10 {
			v.Run.Reroll += uint64(buff.D(7))
		}
	}
	return r, nil
}
func (s *Service) selectReward(ctx command.Context, v *state, typ, id uint64) ([]byte, error) {
	r := v.Run
	beforeGold := r.Gold
	if typ == 3 && r.State == 1 && r.Room > 0 {
		_, rule := s.current(r)
		rate := rule.D(2)
		if rate <= 0 {
			return nil, fail("healing choice unavailable")
		}
		var b []byte
		for i := range r.Characters {
			c := &r.Characters[i]
			if c.HP > 0 {
				c.HP = min(c.MaxHP, c.HP+uint64(float64(c.MaxHP)*rate))
			}
			b = wire.AppendBytes(b, 3, charWire(*c, s.design.Default.V(7)))
		}
		f, rm := s.clearRoom(r)
		return append(b, clearedResponse(r, f, rm, 1, 6, 7)...), nil
	}
	if (r.State != 1 && r.State != 2 && r.State != 8) || typ != r.ChoiceType || !slices.Contains(r.Choice, id) {
		return nil, fail("reward was not offered")
	}
	var b []byte
	if typ == 1 {
		c, a, e := s.addCostume(ctx, v, id)
		if e != nil {
			return nil, e
		}
		if c != nil {
			b = wire.AppendBytes(b, 3, charWire(*c, s.design.Default.V(7)))
		}
		b = wire.AppendBytes(b, 4, costumeWire(*a))
	} else {
		a, e := s.addRelic(v, id)
		if e != nil {
			return nil, e
		}
		b = wire.AppendBytes(b, 5, relicWire(a))
	}
	if r.Remaining > 0 {
		r.Remaining--
	}
	var f uint64
	var room *room
	if r.Remaining > 0 {
		r.State = 1
		wild := uint64(len(r.Costumes)) < s.design.Default.V(45)
		if wild {
			r.State = 8
		}
		if e := s.newChoice(r, typ, r.RelicChoice, wild); e != nil {
			return nil, e
		}
	} else if r.Room == 0 {
		r.State = 0
		r.ChoiceType = 0
		r.Choice = nil
	} else {
		f, room = s.clearRoom(r)
	}
	b = append(b, clearedResponse(r, f, room, 1, 6, 7)...)
	b = wire.AppendBytes(b, 2, choiceWire(r))
	b = wire.AppendVarint(b, 8, r.Gold-beforeGold)
	return wire.AppendVarint(b, 9, r.Reroll), nil
}
