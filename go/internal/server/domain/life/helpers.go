package life

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) citizen(ctx command.Context, v *snapshot, path string, req []byte) ([]byte, error) {
	_, o, err := s.selected(v, msg(req, 2))
	if err != nil {
		return nil, err
	}
	if o.Status != 1 || num(s.design.Row("LifeBuildingObjectTable", o.ID), 2) != 102 {
		return nil, ErrInvalid
	}
	avatar := msg(msg(req, 3), 3)
	if len(avatar) == 0 || num(avatar, 1) == 0 || s.options.ValidateAvatar == nil {
		return nil, ErrInvalid
	}
	if err := s.options.ValidateAvatar(ctx, avatar); err != nil {
		return nil, err
	}
	c, exists := v.Citizens[o.Index]
	if path == "/LifeCitizenRecruit" {
		if exists {
			return nil, ErrInvalid
		}
		c = citizen{Index: v.Next, Slot: o.Index}
		v.Next++
	} else if !exists {
		return nil, ErrInvalid
	}
	c.Avatar = append([]byte(nil), avatar...)
	v.Citizens[o.Index] = c
	return wire.AppendBytes(nil, 1, citizenWire(c)), nil
}
func (s *Service) building(v *snapshot, raw []byte) (*object, error) {
	_, o, err := s.selected(v, raw)
	if err != nil {
		return nil, err
	}
	if o.Status != 1 || num(s.design.Row("LifeBuildingObjectTable", o.ID), 2) != 101 {
		return nil, ErrInvalid
	}
	return o, nil
}
func (s *Service) chooseSpecies() (helper, error) {
	rows := s.design.Tables["LifeHelperSpeciesTable"]
	ids := make([]uint64, 0, len(rows))
	var total uint64
	for key, r := range rows {
		ids = append(ids, key[0])
		total += num(r, 10)
	}
	slices.Sort(ids)
	if total == 0 {
		return helper{}, ErrInvalid
	}
	pick, err := s.options.Sample(total)
	if err != nil || pick >= total {
		return helper{}, ErrInvalid
	}
	var d []byte
	for _, id := range ids {
		r := s.design.Row("LifeHelperSpeciesTable", id)
		w := num(r, 10)
		if pick < w {
			d = r
			break
		}
		pick -= w
	}
	if d == nil {
		return helper{}, ErrInvalid
	}
	avatar := row(1, num(d, 1), 3, num(d, 7), 6, num(d, 5))
	for _, field := range []struct{ design, proto int }{{6, 2}, {8, 5}} {
		values := ints(d, field.design)
		if len(values) == 0 {
			continue
		}
		n, err := s.options.Sample(uint64(len(values)))
		if err != nil || n >= uint64(len(values)) {
			return helper{}, ErrInvalid
		}
		avatar = wire.AppendVarint(avatar, field.proto, values[n])
	}
	return helper{ID: num(d, 9), Avatar: avatar}, nil
}
func (s *Service) helpers(v *snapshot, path string, req []byte) ([]byte, error) {
	if path == "/LifeHelperReward" {
		return s.settle(v, true)
	}
	placeField := 3
	if path == "/LifeHelperGacha" || path == "/LifeHelperGachaDelete" {
		placeField = 2
	}
	if path == "/LifeHelperRecruit" {
		placeField = 4
	}
	o, err := s.building(v, msg(req, placeField))
	if err != nil {
		return nil, err
	}
	if path == "/LifeHelperGachaDelete" {
		if len(v.Candidates[o.Index]) == 0 {
			return nil, ErrInvalid
		}
		delete(v.Candidates, o.Index)
		return nil, nil
	}
	if path == "/LifeHelperGacha" {
		if len(v.Candidates[o.Index]) > 0 {
			var b []byte
			for _, h := range v.Candidates[o.Index] {
				b = wire.AppendBytes(b, 1, helperWire(h))
			}
			return b, nil
		}
		count := num(s.design.Row("LifeDefaultTable", 0), 18)
		if count == 0 || count > 32 {
			return nil, ErrInvalid
		}
		var b []byte
		for range count {
			h, err := s.chooseSpecies()
			if err != nil {
				return nil, err
			}
			h.Index = v.Next
			v.Next++
			h.Slot = o.Index
			v.Candidates[o.Index] = append(v.Candidates[o.Index], h)
			b = wire.AppendBytes(b, 1, helperWire(h))
		}
		return b, nil
	}
	input := msg(req, 2)
	if path == "/LifeHelperRecruit" {
		input = msg(req, 3)
	}
	id := num(input, 1)
	if path == "/LifeHelperRecruit" {
		var selected *helper
		for _, candidate := range v.Candidates[o.Index] {
			if candidate.Index == id {
				h := candidate
				selected = &h
			}
		}
		if selected == nil {
			return nil, ErrInvalid
		}
		occupied := uint64(0)
		for index, h := range v.Helpers {
			if h.Slot == o.Index {
				occupied = index
			}
		}
		if (occupied != 0) != (num(req, 2) != 0) {
			return nil, ErrInvalid
		}
		price := num(s.design.Row("LifeDefaultTable", 0), 19)
		if price > 0 {
			if err := s.spend(v, []gamedata.Reward{{Type: 64, Count: price}}); err != nil {
				return nil, err
			}
		}
		settled, err := s.settle(v, false)
		if err != nil {
			return nil, err
		}
		var b []byte
		if occupied != 0 {
			old := v.Helpers[occupied]
			old.Slot = 0
			old.Work = 0
			old.WorkID = 0
			old.Assigned = 0
			v.Helpers[occupied] = old
			b = wire.AppendBytes(b, 1, helperWire(old))
		}
		v.Helpers[id] = *selected
		delete(v.Candidates, o.Index)
		b = appendConnectedHelpers(b, 1, v)
		return wire.AppendBytes(b, 2, settled), nil
	}
	h, found := v.Helpers[id]
	if !found {
		return nil, ErrInvalid
	}
	if path != "/LifeHelperReconnect" && h.Slot != o.Index {
		return nil, ErrInvalid
	}
	switch path {
	case "/LifeHelperFire":
		bundle, err := s.settle(v, false)
		if err != nil {
			return nil, err
		}
		delete(v.Helpers, id)
		return appendConnectedHelpers(wire.AppendBytes(nil, 1, bundle), 2, v), nil
	case "/LifeHelperReName":
		name := string(msg(input, 4))
		if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 24 || strings.TrimSpace(name) == "" {
			return nil, ErrInvalid
		}
		h.Name = name
		v.Helpers[id] = h
		return nil, nil
	case "/LifeHelperReconnect":
		if h.Slot != 0 {
			return nil, ErrInvalid
		}
		for _, other := range v.Helpers {
			if other.Slot == o.Index {
				return nil, ErrInvalid
			}
		}
		h.Slot = o.Index
		h.Work = 0
		h.WorkID = 0
		h.Assigned = 0
		v.Helpers[id] = h
		return nil, nil
	case "/LifeHelperAssign":
		work, workID := num(input, 6), num(input, 7)
		if work > 3 || work < 3 && workID != 0 || work == 3 && s.design.Row("LifeCropSeedTable", workID) == nil {
			return nil, ErrInvalid
		}
		bundle, err := s.settle(v, false)
		if err != nil {
			return nil, err
		}
		h = v.Helpers[id]
		h.Work, h.WorkID = work, workID
		if work > 0 {
			h.Assigned = s.options.Now().Unix()
		} else {
			h.Assigned = 0
		}
		v.Helpers[id] = h
		return wire.AppendBytes(appendConnectedHelpers(nil, 1, v), 2, bundle), nil
	}
	return nil, ErrInvalid
}

func appendConnectedHelpers(b []byte, field int, v *snapshot) []byte {
	for _, id := range sorted(v.Helpers) {
		if h := v.Helpers[id]; h.Slot != 0 {
			b = wire.AppendBytes(b, field, helperWire(h))
		}
	}
	return b
}

func (s *Service) helperRewards(v *snapshot) ([]gamedata.Reward, map[uint64]int64, int64, error) {
	var gains []gamedata.Reward
	progress := map[uint64]int64{}
	earliest := int64(0)
	now := s.options.Now().Unix()
	defaults := s.design.Row("LifeDefaultTable", 0)
	maxTime := int64(num(defaults, 26))
	for _, id := range sorted(v.Helpers) {
		h := v.Helpers[id]
		if h.Slot == 0 || h.Work == 0 || h.Assigned == 0 {
			continue
		}
		if earliest == 0 || h.Assigned < earliest {
			earliest = h.Assigned
		}
		d := s.design.Row("LifeHelperSpeciesTable", h.ID)
		period := int64(num(defaults, 20))
		statField := map[uint64]int{1: 3, 2: 4, 3: 2}[h.Work]
		if statField == 0 {
			return nil, nil, 0, ErrInvalid
		}
		quantity := num(d, statField)
		var unique []uint64
		if h.Work == 3 {
			crop := s.design.Row("LifeCropSeedTable", h.WorkID)
			period = int64(num(crop, 1))
			unique = []uint64{num(crop, 3)}
		} else {
			unique = s.design.HelperUniques[h.Work]
		}
		if period <= 0 || maxTime <= 0 || quantity == 0 {
			return nil, nil, 0, ErrInvalid
		}
		elapsed := min(max(now-h.Assigned, 0), maxTime)
		cycles := uint64(elapsed / period)
		if cycles == 0 {
			continue
		}
		if quantity > math.MaxInt32/cycles {
			return nil, nil, 0, ErrInvalid
		}
		quantity *= cycles
		if len(unique) == 0 {
			return nil, nil, 0, fmt.Errorf("life: helper has no reward pool")
		}
		for i, u := range unique {
			count := quantity / uint64(len(unique))
			if uint64(i) < quantity%uint64(len(unique)) {
				count++
			}
			if count == 0 {
				continue
			}
			grade := uint64(0)
			if h.Work == 3 {
				var err error
				grade, err = s.grade(num(defaults, 17))
				if err != nil {
					return nil, nil, 0, err
				}
			}
			var itemID uint64
			var err error
			if h.Work == 3 {
				itemID, err = s.uniqueItem(u, grade)
			} else {
				rows := s.design.ItemsByUnique[u]
				if len(rows) == 0 {
					return nil, nil, 0, ErrInvalid
				}
				itemID = num(rows[0], 5)
			}
			if err != nil {
				return nil, nil, 0, err
			}
			gains = append(gains, gamedata.Reward{Type: 66, ID: itemID, Count: count})
		}
		progress[id] = h.Assigned + int64(cycles)*period
		if now-h.Assigned >= maxTime {
			progress[id] = now
		}
	}
	return gains, progress, earliest, nil
}
func rewardPreview(gains []gamedata.Reward) []byte {
	var b []byte
	counts := map[uint64]uint64{}
	for _, r := range gains {
		counts[r.ID] += r.Count
	}
	for _, id := range sorted(counts) {
		b = wire.AppendBytes(b, 1, itemWire(item{ID: id, Type: 66, Count: counts[id]}))
	}
	return b
}
func (s *Service) helperPreview(v *snapshot) ([]byte, error) {
	gains, _, earliest, err := s.helperRewards(v)
	if err != nil {
		return nil, err
	}
	return wire.AppendBytes(row(1, uint64(earliest*1000)), 2, rewardPreview(gains)), nil
}
func (s *Service) settle(v *snapshot, response bool) ([]byte, error) {
	gains, progress, _, err := s.helperRewards(v)
	if err != nil {
		return nil, err
	}
	bundle, err := s.grant(v, gains)
	if err != nil {
		return nil, err
	}
	for id, at := range progress {
		h := v.Helpers[id]
		h.Assigned = at
		v.Helpers[id] = h
	}
	if !response {
		return bundle, nil
	}
	b := wire.AppendBytes(nil, 1, bundle)
	for _, id := range sorted(v.Helpers) {
		b = wire.AppendBytes(b, 2, helperWire(v.Helpers[id]))
	}
	return b, nil
}
