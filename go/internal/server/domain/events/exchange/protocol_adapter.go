package eventexchange

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"fmt"
	"math"
	"sort"
	"strconv"
)

func (s *Service) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
	code, ok := map[string]int{"/EventExchangeInfo": 183, "/EventExchangeReward": 184, "/EventExchangeNextPageOpen": 185}[path]
	if !ok {
		return 0, nil, false, nil
	}

	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, fmt.Errorf("eventexchange: invalid sequence")
	}
	st, err := s.load(ctx)
	if err != nil {
		return code, nil, true, err
	}
	key := commandSession + ":" + strconv.FormatUint(seq, 10)
	fingerprint := append([]byte(path), req...)
	if r, ok := st.Receipts[key]; ok {
		if !bytes.Equal(fingerprint, r.Fingerprint) {
			return code, nil, true, fmt.Errorf("eventexchange: sequence conflict")
		}
		return r.Code, r.Response, true, nil
	}
	var out []byte
	if path == "/EventExchangeInfo" {
		rows := s.registry.List()
		sort.Slice(rows, func(i, j int) bool { return rows[i].UID < rows[j].UID })
		for _, a := range rows {
			_, g, e := s.active(a.UID)
			if e != nil {
				continue
			}
			p := s.init(&st, a, g)
			st.Progress[strconv.FormatUint(a.UID, 10)] = p
			out = wire.AppendBytes(out, 1, progressWire(a.UID, p))
			for _, entry := range g.Page(p.Page) {
				out = wire.AppendBytes(out, 2, countWire(a.UID, g.ID, entry.ID, p.Counts[strconv.FormatUint(entry.ID, 10)]))
			}
		}
		if err = s.save(ctx, st); err != nil {
			return code, nil, true, err
		}
		return code, out, true, nil
	}
	uid, _, e := wire.Varint(req, 2)
	if e != nil || uid == 0 {
		return code, nil, true, fmt.Errorf("eventexchange: invalid event UID")
	}
	a, g, err := s.active(uid)
	if err != nil {
		return code, nil, true, err
	}
	p := s.init(&st, a, g)
	if path == "/EventExchangeNextPageOpen" {
		group, _, e := wire.Varint(req, 3)
		if e != nil || group != g.ID {
			return code, nil, true, fmt.Errorf("eventexchange: group mismatch")
		}
		if !canAdvance(g, p) {
			return code, nil, true, fmt.Errorf("eventexchange: next page locked")
		}
		if err = advance(g, &p); err != nil {
			return code, nil, true, err
		}
	} else {
		count, _, e := wire.Varint(req, 3)
		if e != nil || count == 0 || count > 10 {
			return code, nil, true, fmt.Errorf("eventexchange: count must be 1..10")
		}
		uses, e := parseUses(req)
		if e != nil {
			return code, nil, true, e
		}
		free := len(uses) == 0
		if free {
			if g.FreeCount == 0 || p.Free < count {
				return code, nil, true, fmt.Errorf("eventexchange: free draws exhausted")
			}
			p.Free -= count
		} else {
			if g.Cost.Count > math.MaxInt32/count {
				return code, nil, true, fmt.Errorf("eventexchange: cost overflow")
			}
			want := g.Cost.Count * count
			var total uint64
			for _, item := range uses {
				if item.Type != g.Cost.Type || item.ID != g.Cost.ID || item.Count == 0 || item.Count > math.MaxInt32 || total > math.MaxInt32-item.Count {
					return code, nil, true, fmt.Errorf("eventexchange: invalid consume items")
				}
				total += item.Count
			}
			if total != want {
				return code, nil, true, fmt.Errorf("eventexchange: consume count mismatch")
			}
		}
		var rewards []gamedata.Reward
		var changed []byte
		for range count {
			entries := g.Page(p.Page)
			var weights []uint64
			var sum uint64
			for _, entry := range entries {
				remaining := entry.SetCount - p.Counts[strconv.FormatUint(entry.ID, 10)]
				weight := entry.Ratio
				var draws uint64
				for _, c := range p.Counts {
					draws += c
				}
				if g.UnlockRatio > 0 && draws+1 < g.UnlockRatio {
					weight = entry.LimitedRatio
				}
				if remaining == 0 {
					weight = 0
				}
				if sum > math.MaxUint64-weight {
					return code, nil, true, fmt.Errorf("eventexchange: weight sum overflow")
				}
				sum += weight
				weights = append(weights, weight)
			}
			if sum == 0 {
				return code, nil, true, fmt.Errorf("eventexchange: reward pool exhausted")
			}
			v, e := s.draw(sum)
			if e != nil || v >= sum {
				return code, nil, true, fmt.Errorf("eventexchange: random source failure")
			}
			for i, entry := range entries {
				if v < weights[i] {
					id := strconv.FormatUint(entry.ID, 10)
					p.Counts[id]++
					if entry.KeyType == 1 && p.Counts[id] == entry.SetCount {
						p.Key++
					}
					rewards = append(rewards, entry.Reward)
					changed = wire.AppendBytes(changed, 2, countWire(uid, g.ID, entry.ID, p.Counts[id]))
					break
				}
				v -= weights[i]
			}
			if exhausted(g, p) && (p.Page < g.EndPage || g.Repeat) {
				if e := advance(g, &p); e != nil {
					return code, nil, true, e
				}
			}
		}
		var bundle []byte
		if free {
			bundle, err = s.runtime.Apply(ctx, "eventexchange:"+key, nil, rewards)
		} else {
			currency := g.Cost.Type == 2 || g.Cost.Type == 3 || g.Cost.Type == 4 || g.Cost.Type == 12 || g.Cost.Type == 20
			if currency {
				cost := g.Cost
				cost.Count *= count
				bundle, err = s.runtime.Apply(ctx, "eventexchange:"+key, []gamedata.Reward{cost}, rewards)
			} else {
				rs := make([]gamedata.BattleReward, len(rewards))
				for i, r := range rewards {
					rs[i] = gamedata.BattleReward{Type: r.Type, ID: r.ID, Count: r.Count} //nolint:staticcheck // S1016
				}
				bundle, err = s.runtime.ConsumeAndGrant(ctx, "eventexchange:"+key, uses, rs)
			}
		}
		if err != nil {
			return code, nil, true, err
		}
		out = wire.AppendBytes(out, 1, bundle)
		out = append(out, changed...)
		out = wire.AppendBytes(out, 3, progressWire(uid, p))
	}
	st.Progress[strconv.FormatUint(uid, 10)] = p
	st.Receipts[key] = receipt{fingerprint, out, code}
	if err = s.save(ctx, st); err != nil {
		return code, nil, true, err
	}
	return code, out, true, nil
}

func progressWire(uid uint64, p progress) []byte {
	var out []byte
	for _, f := range []struct {
		n int
		v uint64
	}{{1, uid}, {2, p.Group}, {3, p.Page}, {4, p.Key}, {5, p.Free}, {6, uint64(p.Reset)}} {
		if f.v > 0 {
			out = wire.AppendVarint(out, f.n, f.v)
		}
	}
	return out
}

func countWire(uid, group, id, count uint64) []byte {
	b := wire.AppendVarint(nil, 1, uid)
	b = wire.AppendVarint(b, 2, group)
	b = wire.AppendVarint(b, 3, id)
	if count > 0 {
		b = wire.AppendVarint(b, 4, count)
	}
	return b
}

func parseUses(req []byte) ([]assets.Item, error) {
	var out []assets.Item
	seen := map[uint64]bool{}
	err := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 4 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("eventexchange: malformed use item")
		}
		var item assets.Item
		for n, p := range map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count} {
			v, _, e := wire.Varint(f.Value, n)
			if e != nil {
				return e
			}
			*p = v
		}
		if item.Type == 0 || item.Count == 0 || item.InvenIndex > math.MaxInt64 || seen[item.InvenIndex] {
			return fmt.Errorf("eventexchange: invalid or duplicate consume item")
		}
		seen[item.InvenIndex] = true
		out = append(out, item)
		return nil
	})
	return out, err
}
