package roguelike

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"math"
	"slices"
)

func rewards(rule gamedata.RogueRule, tf, idf, cf int) []gamedata.Reward {
	var out []gamedata.Reward
	for i, t := range rule.A(tf) {
		if i < len(rule.A(idf)) && i < len(rule.A(cf)) && rule.A(cf)[i] > 0 {
			out = append(out, gamedata.Reward{Type: t, ID: rule.A(idf)[i], Count: rule.A(cf)[i]})
		}
	}
	return out
}
func (s *Service) growth(v *state, b []byte) ([]byte, error) {
	a, e := messages(b, 2)
	if e != nil || len(a) != 1 {
		return nil, fail("invalid growth")
	}
	typ, level := n(a[0], 1), n(a[0], 2)
	rule := s.design.Growth.Row(typ, level)
	if rule.Values == nil || level != v.Growth[typ]+1 || v.Run != nil || v.Obsidian < rule.V(8) {
		return nil, fail("growth unavailable or insufficient obsidian")
	}
	v.Obsidian -= rule.V(8)
	v.Growth[typ] = level
	return wire.AppendVarint(wire.AppendVarint(nil, 1, level), 2, uint64(-int64(rule.V(8)))), nil
}
func (s *Service) score(v *state, penalty bool) ([]byte, uint64) {
	r := v.Run
	if r == nil {
		return nil, 0
	}
	var b []byte
	total := uint64(0)
	best := map[uint64]uint64{}
	counts := map[uint64]uint64{}
	for _, id := range s.design.Scores.Groups[0] {
		x := s.design.Scores.Row(0, id)
		count := uint64(0)
		switch x.V(5) {
		case 1:
			count = r.Cleared
		case 2:
			if r.Gold >= x.V(2) {
				count = 1
			}
		case 3:
			if uint64(len(r.Relics)) >= x.V(2) {
				count = 1
			}
		case 4:
			if uint64(len(r.Characters)) <= x.V(2) {
				count = 1
			}
		case 5:
			if uint64(len(r.Characters)) >= x.V(2) {
				count = 1
			}
		case 6:
			if r.Events >= x.V(2) {
				count = 1
			}
		case 7:
			count = r.Elites
		case 8:
			count = r.Bosses
		case 9:
			count = r.Crystal / max(s.design.Default.V(18), 1)
		}
		if count == 0 {
			continue
		}
		score := count * x.V(6)
		typ := x.V(5)
		if typ == 2 || typ == 3 || typ == 6 {
			if score <= best[typ] {
				continue
			}
			best[typ] = score
			counts[typ] = id
			continue
		}
		a := wire.AppendVarint(nil, 1, id)
		a = wire.AppendVarint(a, 2, score)
		a = wire.AppendVarint(a, 3, count)
		b = wire.AppendBytes(b, 1, a)
		total += score
	}
	for _, typ := range []uint64{2, 3, 6} {
		if score := best[typ]; score > 0 {
			a := wire.AppendVarint(nil, 1, counts[typ])
			a = wire.AppendVarint(a, 2, score)
			a = wire.AppendVarint(a, 3, 1)
			b = wire.AppendBytes(b, 1, a)
			total += score
		}
	}
	bonus := s.design.Levels.Row(0, r.Level).D(11)
	total = uint64(float64(total) * bonus)
	total = min(total, math.MaxInt32)
	obs := uint64(float64(total) * s.design.Default.D(29))
	if penalty && !slices.Contains(s.design.Default.A(2), r.Floor-1) {
		obs = uint64(float64(obs) * (1 - s.design.Default.D(25)))
	}
	b = wire.AppendVarint(b, 2, total)
	b = wire.AppendVarint(b, 3, obs)
	return b, obs
}
func (s *Service) publish(ctx command.Context, v *state, score, damage uint64) error {
	r := v.Seasons[v.Season]
	if r.Score > math.MaxInt64-score {
		return fail("season score overflow")
	}
	r.Score += score
	r.Damage = max(r.Damage, damage)
	v.Seasons[v.Season] = r
	if s.options.Publish != nil {
		return s.options.Publish(ctx, v.Season, r.Score, r.Damage, v.NextRun)
	}
	return nil
}
func (s *Service) finish(ctx command.Context, v *state, win, penalty bool, key string) ([]byte, []byte, error) {
	r := v.Run
	if r == nil {
		return nil, nil, fail("no run to finish")
	}
	score, obs := s.score(v, penalty)
	var bundle []byte
	if win {
		record := v.Seasons[v.Season]
		record.Cleared = true
		v.Seasons[v.Season] = record
		v.MaxTry = max(v.MaxTry, r.Level)
		if r.Level > v.MaxReward {
			var grant []gamedata.Reward
			for level := v.MaxReward + 1; level <= r.Level; level++ {
				grant = append(grant, rewards(s.design.Levels.Row(0, level), 10, 9, 8)...)
			}
			var e error
			bundle, e = s.options.Economy.Apply(ctx, key+":clear", nil, grant)
			if e != nil {
				return nil, nil, e
			}
			v.MaxReward = r.Level
		}
	}
	if obs > math.MaxInt32-v.Obsidian {
		return nil, nil, fail("obsidian overflow")
	}
	v.Obsidian += obs
	v.HighestCrystal = max(v.HighestCrystal, r.Crystal)
	if e := s.publish(ctx, v, n(score, 2), r.Crystal); e != nil {
		return nil, nil, e
	}
	total, e := s.total(ctx, v)
	if e != nil {
		return nil, nil, e
	}
	score = wire.AppendVarint(score, 4, total)
	score = wire.AppendVarint(score, 5, v.MaxTry)
	score = wire.AppendVarint(score, 6, v.MaxReward)
	score = wire.AppendVarint(score, 7, r.Crystal)
	v.LastScore = score
	v.Run = nil
	return score, bundle, nil
}
func (s *Service) quick(ctx command.Context, v *state, p Period, b []byte, key string) ([]byte, error) {
	level, count := n(b, 2), n(b, 3)
	rule := s.design.Levels.Row(0, level)
	if !p.Active || v.Run != nil || rule.Values == nil || level > v.MaxTry || count == 0 || count > 100 {
		return nil, fail("quick battle requires previous clear")
	}
	if e := s.options.Tickets(ctx, key, count*s.design.Default.V(30)); e != nil {
		return nil, e
	}
	score := rule.V(7) * count
	obs := uint64(float64(score) * s.design.Default.D(29))
	if obs > math.MaxInt32-v.Obsidian {
		return nil, fail("obsidian overflow")
	}
	v.Obsidian += obs
	v.NextRun++
	if e := s.publish(ctx, v, score, 0); e != nil {
		return nil, e
	}
	a := wire.AppendVarint(nil, 1, 9999)
	a = wire.AppendVarint(a, 2, score)
	a = wire.AppendVarint(a, 3, count)
	out := wire.AppendBytes(nil, 1, a)
	out = wire.AppendVarint(out, 2, score)
	out = wire.AppendVarint(out, 3, obs)
	total, e := s.total(ctx, v)
	if e != nil {
		return nil, e
	}
	out = wire.AppendVarint(out, 4, total)
	out = wire.AppendVarint(out, 5, v.MaxTry)
	out = wire.AppendVarint(out, 6, v.MaxReward)
	return wire.AppendBytes(nil, 1, out), nil
}
func rankWire(a RankEntry) []byte {
	b := append([]byte(nil), a.Profile...)
	b = wire.AppendVarint(b, 1, a.Owner)
	b = wire.AppendVarint(b, 7, a.Rank)
	b = wire.AppendVarint(b, 8, min(a.Score, math.MaxInt32))
	return wire.AppendVarint(b, 10, a.Damage)
}
func (s *Service) rankings(ctx command.Context, v *state, season uint64) ([]RankEntry, RankEntry, error) {
	local := v.Seasons[season]
	entry := RankEntry{Owner: s.options.Owner, Score: local.Score, Damage: local.Damage}
	if s.options.Ranking != nil {
		return s.options.Ranking.Snapshot(ctx, season, entry)
	}
	if entry.Score > 0 {
		entry.Rank = 1
		return []RankEntry{entry}, entry, nil
	}
	return nil, entry, nil
}
func (s *Service) rank(ctx command.Context, v *state, season uint64) ([]byte, error) {
	if season == 0 || season > v.Season {
		return nil, fail("invalid ranking season")
	}
	top, mine, e := s.rankings(ctx, v, season)
	if e != nil {
		return nil, e
	}
	var out []byte
	for _, a := range top {
		out = wire.AppendBytes(out, 1, rankWire(a))
	}
	return wire.AppendBytes(out, 2, rankWire(mine)), nil
}
func (s *Service) seasonReward(ctx command.Context, v *state, key string) ([]byte, error) {
	var season uint64
	for id, r := range v.Seasons {
		if s.seasonSettled(id, v.Season) && !r.Claimed && r.Cleared && r.Score > 0 && (season == 0 || id < season) {
			season = id
		}
	}
	if season == 0 {
		return nil, fail("no completed season reward")
	}
	total := v.Seasons[season].Score
	var e error
	if s.options.Ranking != nil {
		total, e = s.options.Ranking.Total(ctx, season)
		if e != nil {
			return nil, e
		}
	}
	var grants []gamedata.Reward
	for _, id := range s.design.Rewards.Groups[0] {
		rule := s.design.Rewards.Row(0, id)
		if float64(total) >= rule.D(5) {
			grants = append(grants, rewards(rule, 4, 3, 2)...)
		}
	}
	bundle, e := s.options.Economy.Apply(ctx, key, nil, grants)
	if e != nil {
		return nil, e
	}
	r := v.Seasons[season]
	r.Claimed = true
	v.Seasons[season] = r
	_, mine, e := s.rankings(ctx, v, season)
	if e != nil {
		return nil, e
	}
	return wire.AppendBytes(wire.AppendVarint(nil, 1, mine.Rank), 2, bundle), nil
}

func (s *Service) seasonSettled(id, current uint64) bool {
	if s.options.SeasonSettled != nil {
		return s.options.SeasonSettled(id)
	}
	return id < current
}
