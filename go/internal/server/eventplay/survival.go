package eventplay

import (
	"bd2server/internal/server/wire"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

func (s *Service) survivalProgress(req []byte, a *Run) (uint64, uint64, error) {
	char, e := s.design.Row("FieldMiniGameCharTable", 13, a.Char)
	if e != nil {
		return 0, 0, e
	}
	var mapRow []byte
	for _, r := range s.design.Rows("FieldMiniGameMapTable", 2, a.MapGroup) {
		if num(r, 3) == a.Stage {
			mapRow = r
		}
	}
	if mapRow == nil {
		return 0, 0, fmt.Errorf("eventplay: survival map missing")
	}
	monsters := s.design.Rows("FieldMiniGameMonsterTable", 2, num(mapRow, 4))
	if a.Killed == nil {
		a.Killed = map[uint64]uint64{}
	}
	if a.Picked == nil {
		a.Picked = map[uint64]uint64{}
	}
	if a.Available == nil {
		a.Available = map[uint64]uint64{}
	}
	elapsed := uint64(0)
	if uint64(s.now().UnixMilli()) > a.Started {
		elapsed = (uint64(s.now().UnixMilli()) - a.Started) / 1000
	}
	e = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 6 {
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		id, count := num(f.Value, 1), num(f.Value, 2)
		if count == 0 {
			return fmt.Errorf("eventplay: empty kill count")
		}
		var row []byte
		for _, r := range monsters {
			if num(r, 5) == id {
				row = r
				break
			}
		}
		if row == nil {
			return fmt.Errorf("eventplay: monster outside survival map")
		}
		max := num(row, 12)
		repeats := num(row, 11)
		start := num(row, 16)
		interval := num(row, 13)
		if elapsed < start {
			return fmt.Errorf("eventplay: monster not spawned yet")
		}
		waves := uint64(1)
		if interval > 0 {
			waves += (elapsed - start) / interval
		}
		if repeats > 0 && waves > repeats {
			waves = repeats
		}
		if max == 0 || a.Killed[id] > max*waves || count > max*waves-a.Killed[id] {
			return fmt.Errorf("eventplay: kill count exceeds designed spawns")
		}
		a.Killed[id] += count
		box := num(row, 6)
		if box != 0 {
			items, err := s.boxItems(box)
			if err != nil {
				return err
			}
			for _, item := range items {
				a.Available[item] += count
			}
		}
		return nil
	})
	if e != nil {
		return 0, 0, e
	}
	exp, coin := uint64(0), uint64(0)
	healing := uint64(0)
	e = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 5 {
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		id, count := num(f.Value, 1), num(f.Value, 2)
		if count == 0 || count > a.Available[id] {
			return fmt.Errorf("eventplay: item count exceeds committed drops")
		}
		row, e := s.design.Row("FieldMiniGameSurvivalItemTable", 1, id)
		if e != nil {
			return e
		}
		v := num(row, 4)
		if count > math.MaxInt32 || v > math.MaxInt32/count {
			return fmt.Errorf("eventplay: item value overflow")
		}
		a.Available[id] -= count
		a.Picked[id] += count
		switch num(row, 3) {
		case 0:
			healing += v * count
		case 3:
			exp += uint64(math.Floor(float64(v*count) * doubleField(char, 8, 1)))
		case 4:
			coin += uint64(math.Floor(float64(v*count) * doubleField(char, 10, 1)))
		case 5:
			a.SkillCredits += count
		}
		return nil
	})
	if e != nil {
		return 0, 0, e
	}
	boxes, e := list(req, 7)
	if e != nil {
		return 0, 0, e
	}
	for _, id := range boxes {
		if _, e := s.design.Row("FieldMiniGameSurvivalBoxTable", 2, id); e != nil {
			return 0, 0, e
		}
	}
	hp := num(req, 2)
	if hp > a.MaxHP || hp > a.HP+healing {
		return 0, 0, fmt.Errorf("eventplay: invalid survival HP")
	}
	a.HP = hp
	a.Exp += exp
	a.Coin += coin
	a.LevelExp += exp
	if a.Exp > math.MaxInt32 || a.Coin > math.MaxInt32 {
		return 0, 0, fmt.Errorf("eventplay: survival progress exceeds protocol range")
	}
	growth := num(char, 2)
	for {
		var current, next []byte
		for _, r := range s.design.Rows("FieldMiniGameCharLevelTable", 1, growth) {
			if num(r, 2) == a.Level {
				current = r
			}
			if num(r, 2) == a.Level+1 {
				next = r
			}
		}
		if current == nil || next == nil || num(current, 3) == 0 || a.LevelExp < num(current, 3) {
			break
		}
		a.LevelExp -= num(current, 3)
		a.Level++
		a.SkillCredits++
	}
	return exp, coin, nil
}
func doubleField(b []byte, n int, def float64) float64 {
	v := def
	_ = wire.Walk(b, func(f wire.Field) error {
		if f.Number == n && f.Type == 1 {
			v = math.Float64frombits(binary.LittleEndian.Uint64(f.Value))
		}
		return nil
	})
	return v
}
func (s *Service) boxItems(id uint64) ([]uint64, error) {
	r, e := s.design.Row("FieldMiniGameSurvivalBoxTable", 2, id)
	if e != nil {
		return nil, e
	}
	values, e := list(r, 3)
	if e != nil {
		return nil, e
	}
	return values, nil
}
func (s *Service) survivalOffers(a *Run) ([]byte, error) {
	if a.SkillCredits == 0 {
		return nil, fmt.Errorf("eventplay: no earned skill selection")
	}
	if len(a.Offers) > 0 {
		if a.Rerolls == 0 {
			return nil, fmt.Errorf("eventplay: no remaining rerolls")
		}
		a.Rerolls--
	}
	a.Offers = nil
	rows := append([][]byte(nil), s.design.Tables["FieldMiniGameSkillGroupTable"]...)
	sort.Slice(rows, func(i, j int) bool { return num(rows[i], 1) < num(rows[j], 1) })
	for _, r := range rows {
		id := num(r, 1)
		if a.SkillLevels[id] < num(r, 2) {
			a.Offers = append(a.Offers, id)
			if len(a.Offers) == 3 {
				break
			}
		}
	}
	out := wire.AppendVarint(nil, 1, a.Rerolls)
	for _, id := range a.Offers {
		out = wire.AppendVarint(out, 2, id)
	}
	return out, nil
}
