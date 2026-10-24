package life

import (
	"encoding/binary"
	"fmt"
	"math"

	"bd2server/internal/server/protocol/wire"
)

func number(b []byte, field int) float64 {
	var result float64
	_ = wire.Walk(b, func(f wire.Field) error {
		if f.Number == field && f.Type == 1 {
			result = math.Float64frombits(binary.LittleEndian.Uint64(f.Value))
		}
		return nil
	})
	return result
}
func (s *Service) stats(v *snapshot, group uint64) (uint64, uint64, error) {
	if group < 1 || group > 3 {
		return 0, 0, fmt.Errorf("%w: life stat group %d, want 1..3", ErrInvalid, group)
	}
	field := map[uint64]int{1: 23, 2: 27, 3: 13}[group]
	skill, bonus := float64(num(s.design.Row("LifeDefaultTable", 0), field)), float64(0)
	add := func(ids []uint64, count uint64) {
		for _, id := range ids {
			r := s.design.Row("LifeBuffTable", id)
			typ := num(r, 4)
			value := number(r, 5) * float64(count)
			if typ == group*10+1 {
				skill += value
			}
			if typ == group*10+2 {
				bonus += value
			}
		}
	}
	for g := uint64(1); g <= 3; g++ {
		add(ints(s.design.Row("LifeCharGrowthTable", g, v.Growth[(g-1)*2]), 1), 1)
	}
	for g, id := range v.Tools {
		add(ints(s.design.Row("LifeToolTable", g, id), 2), 1)
	}
	for id, count := range v.Counts {
		if num(s.design.Row("LifeObjectGroupTable", id), 12) == 3 {
			count = v.ActiveCounts[id]
		}
		if count > 0 {
			add(ints(s.design.Row("LifeObjectGroupTable", id), 2), count)
		}
	}
	for id, until := range v.Foods {
		if until > s.options.Now().Unix() {
			add(ints(s.design.Row("LifeItemTable", id), 1), 1)
		}
	}
	if math.IsNaN(skill) || math.IsInf(skill, 0) || skill < 0 || skill > math.MaxInt32 || math.IsNaN(bonus) || math.IsInf(bonus, 0) || bonus < 0 || bonus > math.MaxInt32 {
		return 0, 0, fmt.Errorf("%w: life stat group %d skill %v bonus %v out of range", ErrInvalid, group, skill, bonus)
	}
	return uint64(skill), uint64(bonus), nil
}
func (s *Service) grade(table uint64) (uint64, error) {
	r := s.design.Row("LifeCropGradeTable", table)
	grades, weights := ints(r, 1), ints(r, 3)
	if len(grades) == 0 || len(grades) != len(weights) {
		return 0, fmt.Errorf("%w: LifeCropGradeTable %d lists %d grades and %d weights", ErrInvalid, table, len(grades), len(weights))
	}
	var total uint64
	for _, w := range weights {
		if w > math.MaxInt32 || total > math.MaxInt32-w {
			return 0, fmt.Errorf("%w: LifeCropGradeTable %d weights overflow", ErrInvalid, table)
		}
		total += w
	}
	if total == 0 {
		return 0, fmt.Errorf("%w: LifeCropGradeTable %d has no weight", ErrInvalid, table)
	}
	pick, err := s.options.Sample(total)
	if err != nil || pick >= total {
		return 0, fmt.Errorf("%w: crop grade sample %d of %d failed: %v", ErrInvalid, pick, total, err)
	}
	for i, w := range weights {
		if pick < w {
			return grades[i], nil
		}
		pick -= w
	}
	return 0, fmt.Errorf("%w: crop grade sample %d of %d matched no grade in table %d", ErrInvalid, pick, total, table)
}
