package life

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) economy(v *snapshot, path string, req []byte) (body []byte, err error) {
	id, count := num(req, 2), num(req, 3)
	defer func() {
		if err != nil && (path == "/LifeCrafting" || path == "/LifeCooking") {
			err = fmt.Errorf("%w: operation=%s recipe=%d count=%d", err, path, id, count)
		}
	}()
	switch path {
	case "/LifeCrafting":
		d := s.design.Row("LifeCraftingObjectTable", id)
		if d == nil || count == 0 || count > math.MaxInt32 {
			return nil, ErrInvalid
		}
		if !s.hasFunction(v, 11) {
			return nil, ErrInvalid
		}
		cost, err := costs(d, 5, 4, 3, count)
		if err != nil {
			return nil, err
		}
		if err = s.spend(v, cost); err != nil {
			return nil, err
		}
		bundle, err := s.grant(v, []gamedata.Reward{{Type: 65, ID: id, Count: count}})
		return wire.AppendBytes(nil, 1, bundle), err
	case "/LifeCooking":
		d := s.design.Row("LifeCookTable", id)
		if d == nil || count == 0 || count > math.MaxInt32 || !s.hasFunction(v, 13) {
			return nil, ErrInvalid
		}
		unique, quantity := ints(d, 3), ints(d, 2)
		if len(unique) != len(quantity) {
			return nil, ErrInvalid
		}
		required := map[uint64]uint64{}
		for i, u := range unique {
			if quantity[i] > math.MaxInt32/count {
				return nil, ErrInvalid
			}
			required[u] += quantity[i] * count
		}
		cost, err := s.uniqueCosts(v, required, messages(req, 4))
		if err != nil {
			return nil, err
		}
		if err = s.spend(v, cost); err != nil {
			return nil, err
		}
		bundle, err := s.grant(v, []gamedata.Reward{{Type: 66, ID: num(d, 4), Count: count}})
		return wire.AppendBytes(nil, 1, bundle), err
	case "/LifeToolUpgrade":
		group := id
		current, ok := v.Tools[group]
		if !ok || !s.hasFunction(v, 14) {
			return nil, ErrInvalid
		}
		ids := s.design.ToolGroups[group]
		var next uint64
		for i, x := range ids {
			if x == current && i+1 < len(ids) {
				next = ids[i+1]
			}
		}
		d := s.design.Row("LifeToolTable", group, next)
		if d == nil || next == 0 {
			return nil, ErrInvalid
		}
		cost, err := costs(d, 15, 14, 13, 1)
		if err != nil {
			return nil, err
		}
		if price := num(d, 16); price > 0 {
			cost = append(cost, gamedata.Reward{Type: 64, Count: price})
		}
		if err = s.spend(v, cost); err != nil {
			return nil, err
		}
		v.Tools[group] = next
		return row(1, group, 2, next), nil
	case "/LifeEatFood":
		used := messages(req, 2)
		if len(used) != 1 {
			return nil, ErrInvalid
		}
		it := used[0]
		id = num(it, 2)
		if num(it, 3) != 66 || num(it, 4) != 1 {
			return nil, ErrInvalid
		}
		d := s.design.Row("LifeItemTable", id)
		duration := uint64(0)
		for _, buff := range ints(d, 1) {
			duration = max(duration, num(s.design.Row("LifeBuffTable", buff), 1))
		}
		if duration == 0 {
			return nil, ErrInvalid
		}
		if err := s.spend(v, []gamedata.Reward{{Type: 66, ID: id, Count: 1}}); err != nil {
			return nil, err
		}
		v.Foods[id] = s.options.Now().Unix() + int64(duration)
		return wire.AppendBytes(nil, 1, row(1, id, 2, uint64(v.Foods[id]*1000))), nil
	case "/LifeShopBuy":
		return s.buy(v, req)
	case "/LifeShopSell":
		lines := messages(req, 2)
		if len(lines) == 0 || len(lines) > 128 {
			return nil, ErrInvalid
		}
		var cost, gain []gamedata.Reward
		for _, line := range lines {
			group, id, count := num(line, 2), num(line, 3), num(line, 4)
			d := s.design.Row("LifeSellItemTable", group, id)
			if d == nil || count == 0 || count > math.MaxInt32 {
				return nil, ErrInvalid
			}
			amount, price := num(d, 3), num(d, 6)
			if amount == 0 || amount > math.MaxInt32/count || price > math.MaxInt32/count {
				return nil, ErrInvalid
			}
			it := v.Items[itemKey(num(d, 5), num(d, 4))]
			if num(line, 1) != it.Index {
				return nil, ErrInvalid
			}
			cost = append(cost, gamedata.Reward{Type: num(d, 5), ID: num(d, 4), Count: amount * count})
			if price > 0 {
				gain = append(gain, gamedata.Reward{Type: num(d, 8), ID: num(d, 7), Count: price * count})
			}
		}
		if err := s.spend(v, cost); err != nil {
			return nil, err
		}
		bundle, err := s.grant(v, gain)
		return wire.AppendBytes(nil, 1, bundle), err
	}
	return nil, ErrInvalid
}
func (s *Service) hasFunction(v *snapshot, function uint64) bool {
	for id, count := range v.ActiveCounts {
		if count > 0 && num(s.design.Row("LifeBuildingObjectTable", id), 2) == function {
			return true
		}
	}
	return false
}
func (s *Service) uniqueCosts(v *snapshot, required map[uint64]uint64, selected [][]byte) ([]gamedata.Reward, error) {
	var cost []gamedata.Reward
	for _, raw := range selected {
		typ, id, count := num(raw, 3), num(raw, 2), num(raw, 4)
		d := s.design.Row("LifeItemTable", id)
		unique := num(d, 10)
		if typ != 66 || d == nil || count == 0 || count > required[unique] {
			return nil, ErrInvalid
		}
		it := v.Items[itemKey(typ, id)]
		if num(raw, 1) != it.Index {
			return nil, ErrInvalid
		}
		required[unique] -= count
		cost = append(cost, gamedata.Reward{Type: typ, ID: id, Count: count})
	}
	for _, n := range required {
		if n != 0 {
			return nil, ErrInvalid
		}
	}
	return cost, nil
}
func (s *Service) period(kind uint64) string {
	now := s.options.Now().UTC()
	switch kind {
	case 0, 4:
		return "all"
	case 1:
		return now.Format("2006-01-02")
	case 2:
		y, w := now.ISOWeek()
		return fmt.Sprintf("%d-%d", y, w)
	case 3:
		return now.Format("2006-01")
	}
	return "invalid"
}
func (s *Service) buy(v *snapshot, req []byte) ([]byte, error) {
	group, id, count := num(req, 2), num(req, 3), num(req, 4)
	d := s.design.Row("LifeBuyItemTable", group, id)
	if d == nil || count == 0 || count > math.MaxInt32 {
		return nil, ErrInvalid
	}
	quantity, price := num(d, 3), num(d, 6)
	if quantity == 0 || quantity > math.MaxInt32/count || price > math.MaxInt32/count {
		return nil, ErrInvalid
	}
	key := itemKey(group, id)
	p := v.Purchases[key]
	period := s.period(num(d, 10))
	if period == "invalid" {
		return nil, ErrInvalid
	}
	if p.Period != period {
		p = purchase{Period: period}
	}
	limit := num(d, 9)
	if limit > 0 && (p.Count >= limit || count > limit-p.Count) {
		return nil, ErrInvalid
	}
	if price > 0 {
		if err := s.spend(v, []gamedata.Reward{{Type: num(d, 8), ID: num(d, 7), Count: price * count}}); err != nil {
			return nil, err
		}
	}
	bundle, err := s.grant(v, []gamedata.Reward{{Type: num(d, 5), ID: num(d, 4), Count: quantity * count}})
	if err != nil {
		return nil, err
	}
	p.Count += count
	v.Purchases[key] = p
	return wire.AppendBytes(nil, 1, bundle), nil
}
func (s *Service) shopInfo(v *snapshot) []byte {
	now := s.options.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	days := (8 - int(now.Weekday())) % 7
	if days == 0 {
		days = 7
	}
	week := time.Date(now.Year(), now.Month(), now.Day()+days, 0, 0, 0, 0, time.UTC)
	month := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	b := row(2, uint64(day.UnixMilli()), 3, uint64(week.UnixMilli()), 4, uint64(month.UnixMilli()))
	for key, p := range v.Purchases {
		var group, id uint64
		if _, err := fmt.Sscanf(key, "%d:%d", &group, &id); err != nil {
			continue
		}
		d := s.design.Row("LifeBuyItemTable", group, id)
		if d != nil && p.Period == s.period(num(d, 10)) {
			b = wire.AppendBytes(b, 1, row(1, group, 2, id, 3, p.Count))
		}
	}
	return b
}

func (s *Service) uniqueItem(unique, grade uint64) (uint64, error) {
	for _, r := range s.design.ItemsByUnique[unique] {
		if num(r, 3) == grade {
			return num(r, 5), nil
		}
	}
	return 0, fmt.Errorf("life: missing unique item %s", strconv.FormatUint(unique, 10))
}
