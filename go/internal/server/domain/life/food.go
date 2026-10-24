package life

import (
	"fmt"
	"math"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) eatFood(v *snapshot, req []byte) ([]byte, error) {
	used := messages(req, 2)
	if len(used) == 0 {
		return nil, fmt.Errorf("%w: food selection is empty", ErrInvalid)
	}
	counts := map[uint64]uint64{}
	deadlines := map[uint64]int64{}
	now := s.options.Now().Unix()
	for _, raw := range used {
		index, id, typ, count := num(raw, 1), num(raw, 2), num(raw, 3), num(raw, 4)
		invalid := func(reason string) error {
			return fmt.Errorf("%w: food=%d index=%d count=%d reason=%s", ErrInvalid, id, index, count, reason)
		}
		d := s.design.Row("LifeItemTable", id)
		if typ != 66 || d == nil || num(d, 9) != 3 {
			return nil, invalid("not edible food")
		}
		owned := v.Items[itemKey(66, id)]
		if index == 0 || index != owned.Index {
			return nil, invalid("inventory index mismatch")
		}
		if count == 0 || count > math.MaxInt32 || counts[id] > math.MaxInt32-count {
			return nil, invalid("invalid quantity")
		}
		counts[id] += count
		if counts[id] > owned.Count {
			return nil, fmt.Errorf("%w: food=%d index=%d need=%d have=%d", ErrFunds, id, index, counts[id], owned.Count)
		}
		buffs := ints(d, 1)
		if len(buffs) == 0 {
			return nil, invalid("food has no buff")
		}
		duration := uint64(0)
		for _, buff := range buffs {
			seconds := num(s.design.Row("LifeBuffTable", buff), 1)
			if seconds == 0 {
				return nil, invalid("food buff has no duration")
			}
			duration = max(duration, seconds)
		}
		start := max(now, v.Foods[id])
		if start < 0 || start > math.MaxInt64/1000 || duration > uint64(math.MaxInt64/1000-start)/counts[id] {
			return nil, invalid("food deadline overflow")
		}
		deadlines[id] = start + int64(duration*counts[id])
	}
	var costs []gamedata.Reward
	for _, id := range sorted(counts) {
		costs = append(costs, gamedata.Reward{Type: 66, ID: id, Count: counts[id]})
	}
	if err := s.spend(v, costs); err != nil {
		return nil, err
	}
	var body []byte
	for _, id := range sorted(deadlines) {
		v.Foods[id] = deadlines[id]
		body = wire.AppendBytes(body, 1, row(1, id, 2, uint64(deadlines[id]*1000)))
	}
	return body, nil
}
