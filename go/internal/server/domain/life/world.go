package life

import (
	"fmt"
	"math"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) apply(ctx command.Context, v *snapshot, path string, req []byte) ([]byte, error) {
	switch path {
	case "/LifeWorldChoice":
		return s.choose(v, num(req, 2))
	case "/LifeWorldChunkExpand":
		chunk := num(req, 2)
		if v.World == 0 || chunk == 0 || chunk > s.design.MaxChunks || uint64(len(v.Chunks)) >= s.design.MaxChunks || v.Chunks[chunk] {
			return nil, ErrInvalid
		}
		d := s.design.Row("LifeChunkExpandTable", uint64(len(v.Chunks)))
		if d == nil {
			return nil, ErrInvalid
		}
		cost, err := costs(d, 7, 6, 5, 1)
		if err != nil {
			return nil, err
		}
		if err = s.spend(v, cost); err != nil {
			return nil, err
		}
		v.ExpansionRefund = append(v.ExpansionRefund, cost...)
		v.Chunks[chunk] = true
		v.Objects[chunk] = map[string]*object{}
		return wire.AppendBytes(nil, 1, userWire(v)), nil
	case "/LifeWorldObjectPlaceSave":
		return s.place(v, msg(req, 2))
	case "/LifeWorldObjectBuildCompleted":
		return s.complete(v, msg(req, 2))
	case "/LifeWorldObjectUnplaceSave":
		return s.unplace(v, msg(req, 2))
	case "/LifeWorldObjectPositionSave":
		return nil, s.move(v, messages(req, 2))
	case "/LifeWorldObjectPositionDeltaSave":
		return nil, s.delta(v, req)
	case "/LifeWorldObjectGathering":
		return s.gather(v, messages(req, 2))
	case "/LifeSeeding":
		return s.seed(v, messages(req, 2))
	case "/LifeCrafting", "/LifeCooking", "/LifeToolUpgrade", "/LifeEatFood", "/LifeShopBuy", "/LifeShopSell":
		return s.economy(v, path, req)
	case "/LifeCitizenRecruit", "/LifeCitizenAvatarSave":
		return s.citizen(ctx, v, path, req)
	default:
		return s.helpers(v, path, req)
	}
}
func costs(raw []byte, typ, id, count int, mult uint64) ([]gamedata.Reward, error) {
	ts, is, cs := ints(raw, typ), ints(raw, id), ints(raw, count)
	if len(ts) != len(is) || len(is) != len(cs) {
		return nil, ErrInvalid
	}
	var out []gamedata.Reward
	for i, t := range ts {
		if cs[i] == 0 {
			continue
		}
		if mult == 0 || cs[i] > math.MaxInt32/mult {
			return nil, ErrInvalid
		}
		out = append(out, gamedata.Reward{Type: t, ID: is[i], Count: cs[i] * mult})
	}
	return out, nil
}
func (s *Service) choose(v *snapshot, world uint64) ([]byte, error) {
	d := s.design.Row("LifeWorldTable", world)
	if d == nil || world == 0 {
		return nil, ErrInvalid
	}
	if v.World == world {
		return nil, ErrInvalid
	}
	refunds := append([]gamedata.Reward(nil), v.ExpansionRefund...)
	for _, objects := range v.Objects {
		for _, o := range objects {
			refunds = append(refunds, o.Refund...)
		}
	}
	bundle, err := s.grant(v, refunds)
	if err != nil {
		return nil, err
	}
	v.World = world
	v.Chunks = map[uint64]bool{}
	v.Objects = map[uint64]map[string]*object{}
	v.Counts = map[uint64]uint64{}
	v.ActiveCounts = map[uint64]uint64{}
	v.Layers = map[uint64]uint64{}
	v.Citizens = map[uint64]citizen{}
	v.Candidates = map[uint64][]helper{}
	v.ExpansionRefund = nil
	for id, h := range v.Helpers {
		h.Slot = 0
		h.Assigned = 0
		h.Work = 0
		h.WorkID = 0
		v.Helpers[id] = h
	}
	for _, id := range ints(d, 1) {
		v.Chunks[id] = true
		v.Objects[id] = map[string]*object{}
	}
	for _, r := range s.design.WorldDefaults[world] {
		chunk := num(r, 1)
		if !v.Chunks[chunk] {
			return nil, fmt.Errorf("life: default object outside initial chunks")
		}
		o := &object{ID: num(r, 2), X: int32(num(r, 5)), Y: int32(num(r, 6)), Rotate: num(r, 7), Index: v.Next, Status: 1, Inner: map[uint64]*object{}}
		v.Next++
		if err := s.initResources(o); err != nil {
			return nil, err
		}
		if err := s.insert(v, chunk, o); err != nil {
			return nil, err
		}
	}
	b := wire.AppendBytes(nil, 1, bundle)
	for _, id := range sorted(v.Chunks) {
		b = wire.AppendVarint(b, 2, id)
	}
	return b, nil
}
func (s *Service) initResources(o *object) error {
	d := s.design.Row("LifeBuildingObjectTable", o.ID)
	function := num(d, 2)
	if function != 1 && function != 2 {
		return nil
	}
	count := num(d, 5)
	if count > num(d, 7) || count > 256 {
		return ErrInvalid
	}
	for i := range count {
		r, err := s.randomGather(o.ID)
		if err != nil {
			return err
		}
		o.Inner[i] = &object{ID: num(r, 6), Index: i, Status: num(r, 10), Start: s.options.Now().Unix(), Inner: map[uint64]*object{}}
	}
	return nil
}
func (s *Service) randomGather(group uint64) ([]byte, error) {
	variants := s.design.Gathering[group]
	var total uint64
	ids := sorted(variants)
	for _, id := range ids {
		r := variants[id]
		if len(r) > 0 {
			total += num(r[0], 3)
		}
	}
	if total == 0 {
		return nil, ErrInvalid
	}
	pick, err := s.options.Sample(total)
	if err != nil || pick >= total {
		return nil, ErrInvalid
	}
	for _, id := range ids {
		rs := variants[id]
		w := num(rs[0], 3)
		if pick < w {
			return rs[len(rs)-1], nil
		}
		pick -= w
	}
	return nil, ErrInvalid
}
func (s *Service) insert(v *snapshot, chunk uint64, o *object) error {
	if !v.Chunks[chunk] || o.ID == 0 || o.Rotate > 3 || v.Objects[chunk][objectKey(o)] != nil {
		return ErrInvalid
	}
	if v.Objects[chunk] == nil {
		v.Objects[chunk] = map[string]*object{}
	}
	v.Objects[chunk][objectKey(o)] = o
	v.Counts[o.ID]++
	if o.Status > 0 {
		v.ActiveCounts[o.ID]++
	}
	g := s.design.Row("LifeObjectGroupTable", o.ID)
	if num(g, 12) == 4 {
		v.Layers[num(g, 8)]++
	}
	return nil
}
func (s *Service) remove(v *snapshot, chunk uint64, o *object) {
	delete(v.Objects[chunk], objectKey(o))
	v.Counts[o.ID]--
	if o.Status > 0 {
		v.ActiveCounts[o.ID]--
	}
	g := s.design.Row("LifeObjectGroupTable", o.ID)
	if num(g, 12) == 4 {
		v.Layers[num(g, 8)]--
	}
}
func (s *Service) selected(v *snapshot, raw []byte) (uint64, *object, error) {
	chunk := num(raw, 1)
	os := messages(raw, 2)
	if !v.Chunks[chunk] || len(os) != 1 {
		return 0, nil, ErrInvalid
	}
	wanted := parseObject(os[0])
	o := v.Objects[chunk][objectKey(wanted)]
	if o == nil {
		return 0, nil, ErrInvalid
	}
	return chunk, o, nil
}
func (s *Service) place(v *snapshot, raw []byte) ([]byte, error) {
	chunk := num(raw, 1)
	os := messages(raw, 2)
	if !v.Chunks[chunk] || len(os) != 1 {
		return nil, ErrInvalid
	}
	o := parseObject(os[0])
	g := s.design.Row("LifeObjectGroupTable", o.ID)
	if g == nil || num(g, 7) == 0 || o.Rotate > 3 {
		return nil, ErrInvalid
	}
	if group, level := num(g, 14), num(g, 13); group > 0 && (group > 3 || v.Growth[(group-1)*2] < level) {
		return nil, ErrInvalid
	}
	typ := num(g, 12)
	limit := num(g, 9)
	if limit > 0 && v.Counts[o.ID] >= limit {
		return nil, ErrInvalid
	}
	var required []gamedata.Reward
	var err error
	switch typ {
	case 1:
		if v.Counts[o.ID] >= v.Items[itemKey(65, o.ID)].Count {
			return nil, ErrFunds
		}
	case 3:
		d := s.design.Row("LifeBuildingObjectTable", o.ID)
		if d == nil {
			return nil, ErrInvalid
		}
		required, err = costs(d, 10, 9, 8, 1)
		o.Start = s.options.Now().Unix()
		o.End = o.Start + int64(num(d, 11))
		if num(d, 11) == 0 {
			o.Status = 1
			if err := s.initResources(o); err != nil {
				return nil, err
			}
		}
	case 4:
		d := s.design.Row("LifeDecoObjectTable", o.ID)
		if d == nil {
			return nil, ErrInvalid
		}
		required, err = costs(d, 5, 4, 3, 1)
		layer := num(g, 8)
		ld := s.design.Row("LifeChunkExpandTable", uint64(len(v.Chunks)))
		field := map[uint64]int{1: 3, 2: 2, 3: 1}[layer]
		if field == 0 {
			return nil, ErrInvalid
		}
		if maximum := num(ld, field); maximum > 0 && v.Layers[layer] >= maximum {
			return nil, ErrInvalid
		}
		o.Status = 1
	default:
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	if err = s.spend(v, required); err != nil {
		return nil, err
	}
	o.Refund = required
	o.Index = v.Next
	v.Next++
	if err = s.insert(v, chunk, o); err != nil {
		return nil, err
	}
	return wire.AppendBytes(nil, 1, placeWire(chunk, o)), nil
}
func (s *Service) complete(v *snapshot, raw []byte) ([]byte, error) {
	chunk, o, err := s.selected(v, raw)
	if err != nil {
		return nil, err
	}
	if s.design.Row("LifeBuildingObjectTable", o.ID) == nil || o.Status != 0 || s.options.Now().Unix() < o.End {
		return nil, ErrInvalid
	}
	o.Status = 1
	v.ActiveCounts[o.ID]++
	if err := s.initResources(o); err != nil {
		return nil, err
	}
	return wire.AppendBytes(nil, 1, placeWire(chunk, o)), nil
}
func (s *Service) unplace(v *snapshot, raw []byte) ([]byte, error) {
	chunk, o, err := s.selected(v, raw)
	if err != nil {
		return nil, err
	}
	g := s.design.Row("LifeObjectGroupTable", o.ID)
	if num(g, 7) == 0 || v.Counts[o.ID] <= num(g, 10) {
		return nil, ErrInvalid
	}
	bundle, err := s.grant(v, o.Refund)
	if err != nil {
		return nil, err
	}
	s.remove(v, chunk, o)
	delete(v.Citizens, o.Index)
	delete(v.Candidates, o.Index)
	b := wire.AppendBytes(nil, 1, bundle)
	for id, h := range v.Helpers {
		if h.Slot == o.Index {
			h.Slot = 0
			h.Work = 0
			h.WorkID = 0
			h.Assigned = 0
			v.Helpers[id] = h
			b = wire.AppendBytes(b, 2, helperWire(h))
		}
	}
	return b, nil
}
func (s *Service) move(v *snapshot, moves [][]byte) error {
	if len(moves) == 0 || len(moves) > 512 {
		return ErrInvalid
	}
	type movement struct {
		chunk uint64
		o     *object
	}
	out := make([]movement, 0, len(moves))
	for _, raw := range moves {
		before, after := msg(raw, 1), msg(raw, 2)
		chunk, o, err := s.selected(v, before)
		if err != nil {
			return err
		}
		if num(s.design.Row("LifeObjectGroupTable", o.ID), 7) == 0 {
			return ErrInvalid
		}
		os := messages(after, 2)
		if !v.Chunks[num(after, 1)] || len(os) != 1 {
			return ErrInvalid
		}
		p := parseObject(os[0])
		if p.ID != o.ID || p.Rotate > 3 {
			return ErrInvalid
		}
		s.remove(v, chunk, o)
		o.X, o.Y, o.Rotate = p.X, p.Y, p.Rotate
		out = append(out, movement{num(after, 1), o})
	}
	for _, m := range out {
		if err := s.insert(v, m.chunk, m.o); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) delta(v *snapshot, req []byte) error {
	moves := messages(req, 4)
	dx, dy := num(req, 2), num(req, 3)
	x, y := int64(dx>>1)^-int64(dx&1), int64(dy>>1)^-int64(dy&1)
	if len(moves) == 0 {
		return ErrInvalid
	}
	var plans [][]byte
	for _, raw := range moves {
		o := &object{ID: num(raw, 4), X: int32(num(raw, 2)), Y: int32(num(raw, 3)), Rotate: num(raw, 5)}
		if int64(o.X)+x > math.MaxInt32 || int64(o.X)+x < math.MinInt32 || int64(o.Y)+y > math.MaxInt32 || int64(o.Y)+y < math.MinInt32 {
			return ErrInvalid
		}
		before := placeWire(num(raw, 1), o)
		o.X += int32(x)
		o.Y += int32(y)
		after := placeWire(num(raw, 1), o)
		plans = append(plans, wire.AppendBytes(wire.AppendBytes(nil, 1, before), 2, after))
	}
	return s.move(v, plans)
}
func (s *Service) refresh(v *snapshot) error {
	now := s.options.Now().Unix()
	for _, objects := range v.Objects {
		for _, o := range objects {
			d := s.design.Row("LifeBuildingObjectTable", o.ID)
			function := num(d, 2)
			for slot, in := range o.Inner {
				if in.Status == 0 && (function == 1 || function == 2) && now >= in.Start+int64(num(d, 3)) {
					rows := s.design.Gathering[o.ID][in.ID]
					if len(rows) == 0 {
						return ErrInvalid
					}
					in = &object{ID: in.ID, Index: slot, Status: num(rows[0], 10), Start: in.Start + int64(num(d, 3)), Inner: map[uint64]*object{}}
					o.Inner[slot] = in
				}
				if function >= 1 && function <= 3 && in.Status > 0 {
					rows := s.design.Gathering[o.ID][in.ID]
					if len(rows) == 0 {
						return ErrInvalid
					}
					elapsed := now - in.Start
					stage := num(rows[0], 10)
					for i, r := range rows {
						duration := int64(num(r, 11))
						if elapsed < duration || i == len(rows)-1 {
							stage = num(r, 10)
							break
						}
						elapsed -= duration
					}
					in.Status = stage
				}
			}
		}
	}
	return nil
}

func (s *Service) growth(v *snapshot, group, exp uint64) error {
	if group == 0 || group > 3 {
		return ErrInvalid
	}
	i := (group - 1) * 2
	v.Growth[i+1] += exp
	maximum := num(s.design.Row("LifeDefaultTable", 0), 4)
	for v.Growth[i] < maximum {
		need := num(s.design.Row("LifeCharGrowthTable", group, v.Growth[i]), 4)
		if need == 0 || v.Growth[i+1] < need {
			break
		}
		v.Growth[i+1] -= need
		v.Growth[i]++
	}
	return nil
}
func (s *Service) seed(v *snapshot, seeds [][]byte) ([]byte, error) {
	if len(seeds) == 0 || len(seeds) > 128 {
		return nil, ErrInvalid
	}
	var b []byte
	for _, raw := range seeds {
		place := msg(raw, 2)
		chunk, o, err := s.selected(v, place)
		if err != nil {
			return nil, err
		}
		if num(s.design.Row("LifeBuildingObjectTable", o.ID), 2) != 3 || o.Status != 1 || len(o.Inner) != 0 {
			return nil, ErrInvalid
		}
		os := messages(place, 2)
		ins := messages(os[0], 10)
		if len(ins) != 1 {
			return nil, ErrInvalid
		}
		id := num(ins[0], 6)
		d := s.design.Row("LifeCropSeedTable", id)
		rows := s.design.Gathering[o.ID][id]
		if d == nil || len(rows) == 0 {
			return nil, ErrInvalid
		}
		cost := gamedata.Reward{Type: num(d, 6), ID: num(d, 5), Count: num(d, 4)}
		if err := s.spend(v, []gamedata.Reward{cost}); err != nil {
			return nil, err
		}
		now := s.options.Now().Unix()
		o.Inner[0] = &object{ID: id, Index: 0, Status: num(rows[0], 10), Start: now, End: now + int64(num(d, 7)), Inner: map[uint64]*object{}}
		b = wire.AppendBytes(b, 1, placeWire(chunk, o))
	}
	return b, nil
}
func (s *Service) gather(v *snapshot, places [][]byte) (body []byte, err error) {
	var chunk uint64
	var parent, requested, current *object
	defer func() {
		if err == nil {
			return
		}
		if parent == nil {
			err = fmt.Errorf("%w: gathering chunk=%d", err, chunk)
			return
		}
		if requested == nil {
			err = fmt.Errorf("%w: gathering chunk=%d parent=%s", err, chunk, objectKey(parent))
			return
		}
		actualID, actualStatus := uint64(0), uint64(0)
		if current != nil {
			actualID, actualStatus = current.ID, current.Status
		}
		err = fmt.Errorf("%w: gathering chunk=%d parent=%s slot=%d requested_id=%d requested_status=%d actual_id=%d actual_status=%d", err, chunk, objectKey(parent), requested.Index, requested.ID, requested.Status, actualID, actualStatus)
	}()
	if len(places) == 0 || len(places) > 15 {
		return nil, fmt.Errorf("%w: invalid gathering batch size=%d", ErrInvalid, len(places))
	}
	var bundle []byte
	var changed []byte
	newCollections := map[uint64]bool{}
	changedChunk := uint64(0)
	changedObjects := map[string]*object{}
	mixedChunks := false
	for _, raw := range places {
		chunk = num(raw, 1)
		parent, requested, current = nil, nil, nil
		os := messages(raw, 2)
		if !v.Chunks[chunk] || len(os) == 0 {
			return nil, fmt.Errorf("%w: unavailable chunk or empty objects", ErrInvalid)
		}
		for _, requestObject := range os {
			p := parseObject(requestObject)
			parent, requested, current = p, nil, nil
			o := v.Objects[chunk][objectKey(p)]
			if o == nil || o.Status != 1 {
				return nil, fmt.Errorf("%w: parent missing or incomplete", ErrInvalid)
			}
			ins := messages(requestObject, 10)
			if len(ins) != 1 {
				return nil, fmt.Errorf("%w: inner object count=%d", ErrInvalid, len(ins))
			}
			want := parseObject(ins[0])
			want.Status = num(ins[0], 7)
			requested = want
			in := o.Inner[want.Index]
			current = in
			if in == nil || in.ID != want.ID || in.Status == 0 {
				return nil, fmt.Errorf("%w: resource unavailable or changed", ErrInvalid)
			}
			d := s.design.Row("LifeGatheringObjectTable", o.ID, in.ID, in.Status)
			if d == nil {
				return nil, fmt.Errorf("%w: gathering design missing", ErrInvalid)
			}
			if in.End > 0 && s.options.Now().Unix() < in.End {
				return nil, ErrInvalid
			}
			uniques, counts := ints(d, 9), ints(d, 8)
			if len(uniques) != len(counts) {
				return nil, ErrInvalid
			}
			var rewards []gamedata.Reward
			function := num(s.design.Row("LifeBuildingObjectTable", o.ID), 2)
			skill, bonus, err := s.stats(v, function)
			if err != nil {
				return nil, err
			}
			for i, unique := range uniques {
				ids := s.design.ItemsByUnique[unique]
				if len(ids) == 0 {
					return nil, ErrInvalid
				}
				itemID := num(ids[0], 5)
				if function == 3 {
					grade, err := s.grade(min(skill, num(s.design.Row("LifeDefaultTable", 0), 25)))
					if err != nil {
						return nil, err
					}
					itemID, err = s.uniqueItem(unique, grade)
					if err != nil {
						return nil, err
					}
				}
				count := counts[i] + bonus
				if count > math.MaxInt32 {
					return nil, ErrInvalid
				}
				rewards = append(rewards, gamedata.Reward{Type: 66, ID: itemID, Count: count})
				for _, id := range s.design.CollectionsByItem[[2]uint64{66, itemID}] {
					if !v.Collections[id] {
						newCollections[id] = true
					}
				}
			}
			gain, err := s.grant(v, rewards)
			if err != nil {
				return nil, err
			}
			bundle = append(bundle, gain...)
			if err := s.growth(v, num(s.design.Row("LifeBuildingObjectTable", o.ID), 2), num(d, 1)); err != nil {
				return nil, err
			}
			if function == 3 {
				delete(o.Inner, want.Index)
			} else {
				in.Status = 0
				in.Start = s.options.Now().Unix()
				in.End = 0
			}
			if changedChunk == 0 {
				changedChunk = chunk
			} else if changedChunk != chunk {
				mixedChunks = true
			}
			changedObjects[objectKey(o)] = o
		}
	}
	if !mixedChunks {
		changed = placeWire(changedChunk)
		for _, o := range changedObjects {
			changed = wire.AppendBytes(changed, 2, objectWire(o))
		}
	}
	b := wire.AppendBytes(nil, 1, growthWire(v))
	b = wire.AppendBytes(b, 2, bundle)
	if !mixedChunks {
		b = wire.AppendBytes(b, 3, changed)
	}
	for _, id := range sorted(newCollections) {
		b = wire.AppendVarint(b, 4, id)
	}
	return b, nil
}
