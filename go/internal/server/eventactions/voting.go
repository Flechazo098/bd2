package eventactions

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

func (s *Service) voteEvent() (events.Schedule, gamedata.EventActionRow, bool) {
	v, ok := s.find(23)
	if !ok {
		return v, gamedata.EventActionRow{}, false
	}
	r, ok := s.design.Row("VotingEventTable", 6, v.ID)
	return v, r, ok
}
func (s *Service) rounds(v events.Schedule, r gamedata.EventActionRow) (uint64, []gamedata.EventActionRow) {
	var rows []gamedata.EventActionRow
	current := uint64(0)
	for _, x := range s.design.Tables["VotingRoundTable"] {
		if x.V(2) == r.V(14) {
			rows = append(rows, x)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].V(3) < rows[j].V(3) })
	for _, x := range rows {
		if s.now().UnixMilli() >= v.Start+int64(x.V(4))*86400000 {
			current = x.V(3)
		}
	}
	return current, rows
}
func (s *Service) candidates(r gamedata.EventActionRow) []uint64 {
	var ids []uint64
	for _, x := range s.design.Tables["VotingCandidateTable"] {
		if x.V(1) == r.V(10) {
			ids = append(ids, x.V(2))
		}
	}
	slices.Sort(ids)
	return ids
}
func contains(ids []uint64, id uint64) bool {
	return slices.Contains(ids, id)
}
func voteWire(v *vote) []byte {
	b := wire.AppendVarint(nil, 1, v.Round)
	b = wire.AppendVarint(b, 2, v.Candidate)
	b = wire.AppendVarint(b, 3, v.Normal+v.Additional)
	b = wire.AppendVarint(b, 4, v.Normal)
	return wire.AppendVarint(b, 5, v.Additional)
}
func (s *Service) voteHandle(path string, b []byte, identity string) ([]byte, error) {
	v, r, ok := s.voteEvent()
	if !ok {
		if path == "/CharVoteInfo" || path == "/CharVoteSeasonRanking" {
			return nil, nil
		}
		if path == "/CharVoteTotalRanking" {
			return s.totalRanking(val(b, 2)), nil
		}
		return nil, errors.New("eventactions: voting event inactive")
	}
	round, rounds := s.rounds(v, r)
	ids := s.candidates(r)
	ids = s.advancedCandidates(v, r, round, ids)
	switch path {
	case "/CharVoteInfo":
		out := wire.AppendVarint(nil, 1, round)
		for _, id := range ids {
			out = wire.AppendVarint(out, 2, id)
		}
		for k, x := range s.state.Votes {
			var uid, ro, c uint64
			if _, e := fmt.Sscanf(k, "%d/%d/%d", &uid, &ro, &c); e == nil && uid == v.UID {
				out = wire.AppendBytes(out, 3, voteWire(x))
			}
		}
		for k := range s.state.VoteRewards {
			var uid, id uint64
			if _, e := fmt.Sscanf(k, "%d/%d", &uid, &id); e == nil && uid == v.UID {
				x := wire.AppendVarint(nil, 1, v.ID)
				x = wire.AppendVarint(x, 2, id)
				out = wire.AppendBytes(out, 4, x)
			}
		}
		for _, id := range ids {
			if s.state.Favorites[id] {
				out = wire.AppendVarint(out, 5, id)
			}
			if s.state.NormalVoted[key(v.UID, id)] {
				out = wire.AppendVarint(out, 6, id)
			}
		}
		out = wire.AppendVarint(out, 7, uint64(s.now().UTC().Truncate(24*time.Hour).Add(24*time.Hour).UnixMilli()))
		for i, x := range rounds {
			start := v.Start + int64(x.V(4))*86400000
			end := v.End
			if i+1 < len(rounds) {
				end = v.Start + int64(rounds[i+1].V(4))*86400000
			}
			a := wire.AppendVarint(nil, 1, x.V(3))
			a = wire.AppendVarint(a, 2, uint64(start))
			a = wire.AppendVarint(a, 3, uint64(end))
			out = wire.AppendBytes(out, 9, a)
		}
		return out, nil
	case "/CharVoteFavoriteAdd", "/CharVoteFavoriteDelete":
		id := val(b, 2)
		if !contains(ids, id) {
			return nil, errors.New("eventactions: unknown candidate")
		}
		if path == "/CharVoteFavoriteAdd" {
			s.state.Favorites[id] = true
		} else {
			delete(s.state.Favorites, id)
		}
		return nil, nil
	case "/CharVoteSave":
		id, count, typ := val(b, 2), val(b, 4), val(b, 5)
		if !contains(ids, id) || count == 0 || count > 2147483647 || typ > 1 || round == 0 {
			return nil, errors.New("eventactions: invalid vote")
		}
		normalKey := key(v.UID, id)
		if typ == 0 && (s.state.NormalVoted[normalKey] || count != 1) {
			return nil, errors.New("eventactions: daily normal vote exhausted")
		}
		cost := gamedata.Reward{Type: r.V(9), ID: r.V(8), Count: r.V(7) * count}
		if typ == 1 {
			cost = gamedata.Reward{Type: r.V(3), ID: r.V(2), Count: r.V(1) * count}
		}
		// A first vote may use the additional ticket when the normal ticket
		// is unavailable; the client still sends vote_type=0.
		if typ == 0 {
			var submittedID uint64
			_ = wire.Walk(b, func(f wire.Field) error {
				if f.Number == 3 && f.Type == 2 {
					submittedID = val(f.Value, 2)
				}
				return nil
			})
			if submittedID == r.V(2) {
				cost = gamedata.Reward{Type: r.V(3), ID: r.V(2), Count: r.V(1) * count}
			}
		}
		if cost.Type == 0 || cost.Count == 0 {
			return nil, errors.New("eventactions: missing vote cost")
		}
		vk := key(v.UID, round, id)
		current := s.state.Votes[vk]
		if current == nil {
			current = &vote{Round: round, Candidate: id}
		}
		var total uint64
		for k, x := range s.state.Votes {
			var uid, ro, c uint64
			if _, e := fmt.Sscanf(k, "%d/%d/%d", &uid, &ro, &c); e == nil && uid == v.UID {
				total += x.Normal + x.Additional
			}
		}
		var rewards []gamedata.Reward
		var newIDs []uint64
		for _, rr := range s.design.Tables["VotingCountRewardTable"] {
			if rr.V(1) == r.V(11) && rr.V(6) <= total+count && !s.state.VoteRewards[key(v.UID, rr.V(2))] {
				rewards = append(rewards, rr.Rewards...)
				newIDs = append(newIDs, rr.V(2))
			}
		}
		bundle, e := s.economy.Apply(identity, []gamedata.Reward{cost}, rewards)
		if e != nil {
			return nil, e
		}
		s.state.Votes[vk] = current
		if typ == 0 {
			current.Normal += count
			s.state.NormalVoted[normalKey] = true
		} else {
			current.Additional += count
		}
		out := wire.AppendBytes(nil, 1, bundle)
		for _, id := range newIDs {
			s.state.VoteRewards[key(v.UID, id)] = true
			x := wire.AppendVarint(nil, 1, v.ID)
			x = wire.AppendVarint(x, 2, id)
			out = wire.AppendBytes(out, 2, x)
		}
		out = wire.AppendBytes(out, 4, voteWire(current))
		return out, nil
	case "/CharVoteRanking":
		wanted := val(b, 2)
		if wanted == 0 {
			wanted = round
		}
		out := wire.AppendVarint(nil, 1, v.ID)
		out = wire.AppendVarint(out, 2, wanted)
		for _, x := range s.rankRows(v.UID, wanted, ids) {
			out = wire.AppendBytes(out, 3, x)
		}
		return out, nil
	case "/CharVoteTotalRanking":
		return s.totalRanking(val(b, 2)), nil
	case "/CharVoteSeasonRanking":
		var out []byte
		for _, schedule := range s.registry.List() {
			if schedule.Type != 23 {
				continue
			}
			for _, id := range ids {
				var total uint64
				for k, x := range s.state.Votes {
					var uid, ro, c uint64
					if _, e := fmt.Sscanf(k, "%d/%d/%d", &uid, &ro, &c); e == nil && uid == schedule.UID && c == id {
						total += x.Normal + x.Additional
					}
				}
				if total == 0 {
					continue
				}
				x := wire.AppendVarint(nil, 1, schedule.ID)
				x = wire.AppendVarint(x, 2, id)
				x = wire.AppendVarint(x, 3, total)
				x = wire.AppendVarint(x, 4, total)
				out = wire.AppendBytes(out, 1, x)
			}
		}
		return out, nil
	}
	return nil, errors.New("eventactions: unknown voting operation")
}
func (s *Service) rankRows(uid, round uint64, ids []uint64) [][]byte {
	type result struct{ id, count, last uint64 }
	var rows []result
	var totals map[uint64]uint64
	if s.voteTotals != nil {
		totals, _ = s.voteTotals(uid, round)
	}
	for _, id := range ids {
		r := result{id: id}
		for k, x := range s.state.Votes {
			var u, ro, c uint64
			if _, e := fmt.Sscanf(k, "%d/%d/%d", &u, &ro, &c); e == nil && u == uid && c == id && (round == 0 || round == ro) {
				r.count += x.Normal + x.Additional
				if ro > r.last {
					r.last = ro
				}
			}
		}
		if totals != nil {
			r.count = totals[id]
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count == rows[j].count {
			return rows[i].id < rows[j].id
		}
		return rows[i].count > rows[j].count
	})
	var out [][]byte
	var lastCount, rank uint64
	for i, r := range rows {
		if i == 0 || r.count != lastCount {
			rank = uint64(i + 1)
		}
		lastCount = r.count
		x := wire.AppendVarint(nil, 1, r.id)
		x = wire.AppendVarint(x, 2, r.count)
		x = wire.AppendVarint(x, 3, rank)
		x = wire.AppendVarint(x, 4, r.last)
		out = append(out, x)
	}
	return out
}

func (s *Service) advancedCandidates(v events.Schedule, event gamedata.EventActionRow, round uint64, ids []uint64) []uint64 {
	for previous := uint64(1); previous < round; previous++ {
		var count uint64
		for _, r := range s.design.Tables["VotingRoundTable"] {
			if r.V(2) == event.V(14) && r.V(3) == previous {
				count = r.V(1)
			}
		}
		if count == 0 {
			continue
		}
		ranked := s.rankRows(v.UID, previous, ids)
		var next []uint64
		for _, b := range ranked {
			if val(b, 3) <= count {
				next = append(next, val(b, 1))
			}
		}
		ids = next
	}
	return ids
}
func (s *Service) totalRanking(event uint64) []byte {
	out := wire.AppendVarint(nil, 1, event)
	for _, v := range s.registry.List() {
		if v.Type != 23 || v.ID != event {
			continue
		}
		r, ok := s.design.Row("VotingEventTable", 6, event)
		if !ok {
			continue
		}
		for _, row := range s.rankRows(v.UID, 0, s.candidates(r)) {
			out = wire.AppendBytes(out, 2, row)
		}
	}
	return out
}
