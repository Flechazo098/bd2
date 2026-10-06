package eventgames

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"fmt"
	"math"
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
func rewardCount(d *gamedata.EventGame, count uint64) uint64 {
	max := uint64(0)
	for _, r := range d.Cells {
		if r.Count > max {
			max = r.Count
		}
	}
	if count > max {
		return max
	}
	return count
}
func completeLine(g *GameState, n, typ, index uint64) bool {
	if n == 0 {
		return false
	}
	for i := range n {
		var pos uint64
		switch typ {
		case 1:
			pos = index*n + i
		case 2:
			pos = i*n + index
		case 0:
			if index == 0 {
				pos = i*n + i
			} else {
				pos = i*n + (n - 1 - i)
			}
		default:
			return false
		}
		if pos >= uint64(len(g.Board)) || !contains(g.Opened, pos) {
			return false
		}
	}
	return true
}
