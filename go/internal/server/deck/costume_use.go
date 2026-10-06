package deck

import (
	"bd2server/internal/server/wire"
	"fmt"
	"math"
)

func (s *Store) handleCostumeUse(req []byte) (int, []byte, bool, error) {
	fail := func(e error) (int, []byte, bool, error) { return 41, nil, true, e }
	if e := checkSeq(req); e != nil {
		return fail(e)
	}
	assignments := map[uint64]uint64{}
	e := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("deck: invalid costume use entry")
		}
		cost, _, e := wire.Varint(f.Value, 1)
		if e != nil || cost == 0 || cost > math.MaxInt64 {
			return fmt.Errorf("deck: invalid costume index")
		}
		char, _, e := wire.Varint(f.Value, 2)
		if e != nil || char == 0 || char > math.MaxInt64 {
			return fmt.Errorf("deck: invalid costume character")
		}
		if _, ok := assignments[char]; ok {
			return fmt.Errorf("deck: repeated costume character")
		}
		assignments[char] = cost
		return nil
	})
	if e != nil {
		return fail(e)
	}
	if len(assignments) == 0 {
		return fail(fmt.Errorf("deck: missing costume assignments"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.characters != nil {
		if s.collection == nil {
			return fail(fmt.Errorf("deck: costume collection unavailable"))
		}
		for char, cost := range assignments {
			if _, ok := s.characters.Find(char); !ok {
				return fail(fmt.Errorf("deck: unknown costume character"))
			}
			c, ok := s.collection.CostumeByIndex(cost)
			if !ok || c.UseChar != char {
				return fail(fmt.Errorf("deck: costume not owned by character"))
			}
		}
		if _, e = s.characters.ApplyPresetCostumes(assignments); e != nil {
			return fail(e)
		}
	}
	n := clone(s.state)
	for char, cost := range assignments {
		n.Costumes[char] = cost
	}
	return 41, nil, true, s.commit(n)
}
