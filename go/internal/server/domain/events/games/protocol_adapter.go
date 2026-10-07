package eventgames

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func (s *Service) play(path string, req []byte, g *GameState, d *gamedata.EventGame) ([]byte, uint64, map[int][]gamedata.BattleReward, error) {
	var body []byte
	cost := uint64(1)
	rewards := map[int][]gamedata.BattleReward{}
	fail := func(e error) ([]byte, uint64, map[int][]gamedata.BattleReward, error) { return nil, 0, nil, e }
	switch d.Type {
	case 12:
		if len(d.Moves) == 0 {
			return fail(fmt.Errorf("eventgames: move rules missing"))
		}
		for _, m := range d.Moves {
			if m.Max < m.Min {
				return fail(fmt.Errorf("eventgames: invalid move range"))
			}
			x, e := s.sample(m.Max - m.Min + 1)
			if e != nil {
				return fail(e)
			}
			if x >= m.Max-m.Min+1 {
				return fail(fmt.Errorf("eventgames: sampler out of range"))
			}
			steps := m.Min + x
			v := wire.AppendVarint(nil, 1, m.ID)
			v = wire.AppendVarint(v, 2, steps)
			body = wire.AppendBytes(body, 2, v)
			old := g.Clear
			g.Clear += (g.Position + steps) / uint64(len(d.Cells))
			g.Position = (g.Position + steps) % uint64(len(d.Cells))
			for _, r := range d.Complete {
				if r.Count > old && r.Count <= g.Clear {
					rewards[3] = append(rewards[3], r.Rewards...)
				}
			}
		}
		rewards[4] = append(rewards[4], d.Cells[g.Position].Rewards...)
	case 13:
		plays := scalar(req, 4)
		if plays == 0 || plays > uint64(len(g.Board)-len(g.Opened)) {
			return fail(fmt.Errorf("eventgames: invalid bingo play count"))
		}
		cost = plays
		for range plays {
			var available []uint64
			for pos := range g.Board {
				if !contains(g.Opened, uint64(pos)) {
					available = append(available, uint64(pos))
				}
			}
			pick, e := s.sample(uint64(len(available)))
			if e != nil {
				return fail(e)
			}
			if pick >= uint64(len(available)) {
				return fail(fmt.Errorf("eventgames: sampler out of range"))
			}
			pos := available[pick]
			g.Opened = append(g.Opened, pos)
			body = wire.AppendVarint(body, 3, pos)
			for _, r := range d.Cells {
				if r.ID == g.Board[pos] && r.Count == rewardCount(d, g.Clear) {
					rewards[5] = append(rewards[5], r.Rewards...)
				}
			}
		}
		for _, r := range d.Lines {
			if r.Count != rewardCount(d, g.Clear) {
				continue
			}
			key := r.LineType*1000 + r.LineIndex
			if contains(g.Lines, key) {
				continue
			}
			if completeLine(g, d.Columns, r.LineType, r.LineIndex) {
				g.Lines = append(g.Lines, key)
				v := wire.AppendVarint(nil, 1, r.LineType)
				v = wire.AppendVarint(v, 2, r.LineIndex)
				body = wire.AppendBytes(body, 4, v)
				rewards[6] = append(rewards[6], r.Rewards...)
			}
		}
		if len(g.Opened) == len(g.Board) {
			g.Clear++
			for _, r := range d.Complete {
				if r.Count == g.Clear {
					rewards[7] = append(rewards[7], r.Rewards...)
				}
			}
			if e := s.initialize(g, d); e != nil {
				return fail(e)
			}
		}
	case 17:
		if path == "/MiniPuzzleRenew" {
			wordReady := false
			for _, r := range d.Complete {
				if r.Count != g.Clear {
					continue
				}
				wordReady = true
				for _, id := range r.Members {
					if !contains(g.Opened, id) {
						return fail(fmt.Errorf("eventgames: puzzle word incomplete"))
					}
				}
			}
			if !wordReady {
				return fail(fmt.Errorf("eventgames: puzzle word rules missing"))
			}
			max := uint64(0)
			for _, r := range d.Cells {
				if r.Count > max {
					max = r.Count
				}
			}
			if g.Clear >= max {
				return fail(fmt.Errorf("eventgames: final puzzle completed"))
			}
			g.Clear++
			if e := s.initialize(g, d); e != nil {
				return fail(e)
			}
			return body, 0, rewards, nil
		}
		var opens []uint64
		if path == "/MiniPuzzleAllOpen" {
			for _, id := range g.Board {
				if !contains(g.Opened, id) {
					opens = append(opens, id)
				}
			}
		} else {
			id := scalar(req, 3)
			if !contains(g.Board, id) || contains(g.Opened, id) {
				return fail(fmt.Errorf("eventgames: puzzle tile unavailable"))
			}
			opens = []uint64{id}
		}
		if len(opens) == 0 {
			return fail(fmt.Errorf("eventgames: puzzle already open"))
		}
		cost = uint64(len(opens))
		for _, id := range opens {
			g.Opened = append(g.Opened, id)
			for _, r := range d.Cells {
				if r.ID == id && r.Count == g.Clear {
					rewards[4] = append(rewards[4], r.Rewards...)
					if r.Slot != 0 {
						v := wire.AppendVarint(nil, 1, id)
						v = wire.AppendVarint(v, 2, r.Slot)
						body = wire.AppendBytes(body, 3, v)
					}
				}
			}
		}
		for _, r := range d.Complete {
			if r.Count != g.Clear || contains(g.Lines, r.ID) {
				continue
			}
			all := true
			for _, id := range r.Members {
				all = all && contains(g.Opened, id)
			}
			if all {
				g.Lines = append(g.Lines, r.ID)
				rewards[5] = append(rewards[5], r.Rewards...)
			}
		}
	case 19:
		n := scalar(req, 4)
		typ := scalar(req, 3)
		if n == 0 || n > 100 || typ > 1 {
			return fail(fmt.Errorf("eventgames: invalid roulette draw"))
		}
		if typ == 0 {
			if n > g.Free {
				return fail(fmt.Errorf("eventgames: insufficient free draws"))
			}
			g.Free -= n
			cost = 0
		} else {
			cost = n
			if d.MaxConsume > 0 && n > d.MaxConsume {
				return fail(fmt.Errorf("eventgames: roulette draw exceeds limit"))
			}
		}
		for range n {
			force := d.Pity > 0 && g.SinceSpecial+1 >= d.Pity
			var candidates []gamedata.EventGameReward
			total := uint64(0)
			for _, r := range d.Cells {
				if force && r.Slot != 1 {
					continue
				}
				w := r.Weight
				if g.Special {
					w = r.Weight2
				}
				if w == 0 {
					continue
				}
				if total > math.MaxUint64-w {
					return fail(fmt.Errorf("eventgames: roulette weights overflow"))
				}
				total += w
				candidates = append(candidates, r)
			}
			if total == 0 {
				return fail(fmt.Errorf("eventgames: roulette eligible pool empty"))
			}
			x, e := s.sample(total)
			if e != nil {
				return fail(e)
			}
			if x >= total {
				return fail(fmt.Errorf("eventgames: sampler out of range"))
			}
			var chosen gamedata.EventGameReward
			for _, r := range candidates {
				w := r.Weight
				if g.Special {
					w = r.Weight2
				}
				if x < w {
					chosen = r
					break
				}
				x -= w
			}
			g.Tries++
			g.SinceSpecial++
			if chosen.Slot == 1 {
				g.Special = true
				g.SinceSpecial = 0
			}
			rewards[2] = append(rewards[2], chosen.Rewards...)
			v := wire.AppendVarint(nil, 1, d.RewardGroup)
			v = wire.AppendVarint(v, 2, chosen.ID)
			body = wire.AppendBytes(body, 4, v)
			for _, r := range d.Complete {
				if r.Count == g.Tries {
					rewards[3] = append(rewards[3], r.Rewards...)
				}
			}
		}
	}
	return body, cost, rewards, nil
}

func nums(b []byte, n int) ([]uint64, error) {
	var out []uint64
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		for p := f.Value; len(p) > 0; {
			v, k := binary.Uvarint(p)
			if k <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, v)
			p = p[k:]
		}
		return nil
	})
	return out, e
}

func scalar(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }

func (s *Service) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
	r, ok := routes[path]
	if !ok {
		return 0, nil, false, nil
	}

	fail := func(e error) (int, []byte, bool, error) { return r.Code, nil, true, e }
	if e := wire.Walk(req, func(f wire.Field) error {
		if f.Type == 0 && f.Number != 3 {
			v, _ := binary.Uvarint(f.Value)
			if v > math.MaxInt32 {
				return wire.ErrMalformed
			}
		}
		return nil
	}); e != nil {
		return fail(e)
	}
	seq := scalar(req, 1)
	if seq == 0 || seq > math.MaxInt32 {
		return fail(fmt.Errorf("eventgames: invalid sequence"))
	}
	key := fmt.Sprintf("%s:%s:%d", commandSession, path, seq)
	if v, ok := s.state.Replies[key]; ok {
		if !bytes.Equal(v.Request, req) {
			return fail(fmt.Errorf("eventgames: changed request retry"))
		}
		return r.Code, v.Body, true, nil
	}
	raw, _ := json.Marshal(s.state)
	var next snapshot
	_ = json.Unmarshal(raw, &next)
	uids, e := nums(req, 2)
	if e != nil || len(uids) == 0 && !strings.HasSuffix(path, "Info") {
		return fail(fmt.Errorf("eventgames: missing schedule"))
	}
	if len(uids) == 0 {
		return r.Code, nil, true, nil
	}
	if !strings.HasSuffix(path, "Info") && len(uids) != 1 {
		return fail(fmt.Errorf("eventgames: operation requires one schedule"))
	}
	var out []byte
	seen := map[uint64]bool{}
	for _, uid := range uids {
		if uid == 0 || seen[uid] {
			return fail(fmt.Errorf("eventgames: invalid duplicate schedule"))
		}
		seen[uid] = true
		c, e := s.resolver.Resolve(uid)
		if e != nil {
			return fail(e)
		}
		if c.UID != uid || c.Type != r.Kind {
			return fail(fmt.Errorf("eventgames: schedule/game mismatch"))
		}
		d, e := s.load(c.Type, c.ID)
		if e != nil {
			return fail(e)
		}
		k := strconv.FormatUint(uid, 10)
		g, exists := next.Games[k]
		if !exists {
			g = GameState{UID: uid, Type: c.Type, ID: c.ID, Free: d.Free}
			if e = s.initialize(&g, d); e != nil {
				return fail(e)
			}
		}
		if g.Type != c.Type || g.ID != c.ID {
			return fail(fmt.Errorf("eventgames: saved game identity changed"))
		}
		now := uint64(s.now().UnixMilli())
		if c.Type == 19 && g.Reset <= now {
			g.Free = d.Free
			g.Reset = (now/86400000 + 1) * 86400000
		}
		if strings.HasSuffix(path, "Info") {
			out = wire.AppendBytes(out, 1, stateWire(g, d))
			next.Games[k] = g
			continue
		}
		if int64(now) < c.Start || int64(now) >= c.End {
			return fail(fmt.Errorf("eventgames: event is closed"))
		}
		before := stateWire(g, d)
		body, cost, rewards, e := s.play(path, req, &g, d)
		if e != nil {
			return fail(e)
		}
		consumeField := 3
		if path == "/MiniPuzzleOpen" {
			consumeField = 4
		}
		if path == "/MiniGameRouletteDraw" {
			consumeField = 5
		}
		items, e := consume(req, consumeField, d, cost)
		if e != nil {
			return fail(e)
		}
		bundles := map[int][]byte{}
		fields := []int{3, 4, 5, 6, 7}
		if c.Type == 19 {
			fields = []int{2, 3}
		}
		for _, field := range fields {
			rr := rewards[field]
			if len(rr) == 0 {
				continue
			}
			consumed := []assets.Item(nil)
			if len(items) > 0 {
				consumed = items
				items = nil
			}
			b, e := s.rewards.ConsumeAndGrant(ctx, fmt.Sprintf("eventgames:%s:%d", key, field), consumed, rr)
			if e != nil {
				return fail(e)
			}
			bundles[field] = b
		}
		if len(items) > 0 {
			if _, e = s.rewards.ConsumeAndGrant(ctx, "eventgames:"+key+":cost", items, nil); e != nil {
				return fail(e)
			}
		}
		switch c.Type {
		case 12:
			body = wire.AppendBytes(body, 1, stateWire(g, d))
		case 13, 17:
			body = wire.AppendBytes(body, 1, before)
			body = wire.AppendBytes(body, 2, stateWire(g, d))
		case 19:
			body = wire.AppendBytes(body, 1, stateWire(g, d))
		}
		for _, field := range fields {
			if b := bundles[field]; len(b) > 0 {
				body = wire.AppendBytes(body, field, b)
			}
		}
		out = body
		next.Games[k] = g
	}
	next.Replies[key] = reply{append([]byte(nil), req...), out}
	payload, e := json.Marshal(next)
	if e != nil {
		return fail(e)
	}
	if e = s.store.Save(ctx.State, "eventgames", payload); e != nil {
		return fail(e)
	}
	s.state = next
	return r.Code, out, true, nil
}

func consume(req []byte, n int, d *gamedata.EventGame, count uint64) ([]assets.Item, error) {
	var out []assets.Item
	total := uint64(0)
	seen := map[uint64]bool{}
	e := wire.Walk(req, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		id, typ, qty, index := scalar(f.Value, 2), scalar(f.Value, 3), scalar(f.Value, 4), scalar(f.Value, 1)
		if count == 0 || id != d.CostID || typ != d.CostType || qty == 0 || index == 0 || seen[index] || qty > math.MaxInt32 || total > math.MaxUint64-qty {
			return fmt.Errorf("eventgames: invalid submitted cost")
		}
		seen[index] = true
		total += qty
		out = append(out, assets.Item{InvenIndex: index, ID: id, Type: typ, Count: qty})
		return nil
	})
	if e != nil {
		return nil, e
	}
	if d.Cost > 0 && count > math.MaxUint64/d.Cost || total != count*d.Cost {
		return nil, fmt.Errorf("eventgames: cost does not match design")
	}
	return out, nil
}

func stateWire(g GameState, d *gamedata.EventGame) []byte {
	b := wire.AppendVarint(nil, 1, g.UID)
	switch g.Type {
	case 12:
		b = wire.AppendVarint(b, 2, d.ScaffoldGroup)
		b = wire.AppendVarint(b, 3, d.Cells[g.Position].ID)
		b = wire.AppendVarint(b, 4, g.Clear)
	case 13, 17:
		b = wire.AppendVarint(b, 2, g.Clear)
		for _, v := range g.Board {
			b = wire.AppendVarint(b, 3, v)
		}
		for _, v := range g.Opened {
			b = wire.AppendVarint(b, 4, v)
		}
	case 19:
		b = wire.AppendVarint(b, 2, g.Free)
		b = wire.AppendVarint(b, 3, g.Reset)
		if g.Special {
			b = wire.AppendVarint(b, 4, 1)
		}
		b = wire.AppendVarint(b, 5, g.Tries)
	}
	return b
}
