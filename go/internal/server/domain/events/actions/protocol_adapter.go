package eventactions

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func voteWire(v *vote) []byte {
	b := wire.AppendVarint(nil, 1, v.Round)
	b = wire.AppendVarint(b, 2, v.Candidate)
	b = wire.AppendVarint(b, 3, v.Normal+v.Additional)
	b = wire.AppendVarint(b, 4, v.Normal)
	return wire.AppendVarint(b, 5, v.Additional)
}

func (s *Service) voteHandle(ctx command.Context, path string, b []byte, identity string) ([]byte, error) {
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
		bundle, e := s.economy.Apply(ctx, identity, []gamedata.Reward{cost}, rewards)
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

func decodeTacticsDeck(request []byte) ([]tacticsDeckEntry, error) {
	var entries []tacticsDeckEntry
	indices, characters, positions, sequences := map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return errors.New("eventactions: malformed tactics deck entry")
		}
		seen := map[int]bool{}
		if err := wire.Walk(f.Value, func(v wire.Field) error {
			if v.Number >= 1 && v.Number <= 5 {
				if v.Type != 0 || seen[v.Number] {
					return errors.New("eventactions: malformed tactics deck field")
				}
				seen[v.Number] = true
			}
			return nil
		}); err != nil {
			return err
		}
		e := tacticsDeckEntry{val(f.Value, 1), val(f.Value, 2), val(f.Value, 3), val(f.Value, 4), val(f.Value, 5)}
		// These indices are client-generated virtual identities, not owned
		// inventory. Still enforce their signed protocol range and uniqueness.
		if e.index == 0 || e.index > math.MaxInt64 || e.character == 0 || e.character > math.MaxInt32 || e.costume == 0 || e.costume > math.MaxInt32 || e.position >= tacticsGridSize || e.sequence == 0 || e.sequence > tacticsPartySize || indices[e.index] || characters[e.character] || positions[e.position] || sequences[e.sequence] {
			return errors.New("eventactions: invalid tactics deck")
		}
		indices[e.index], characters[e.character], positions[e.position], sequences[e.sequence] = true, true, true, true
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 || len(entries) > tacticsPartySize {
		return nil, errors.New("eventactions: invalid tactics deck size")
	}
	for sequence := uint64(1); sequence <= uint64(len(entries)); sequence++ {
		if !sequences[sequence] {
			return nil, errors.New("eventactions: invalid tactics deck sequence")
		}
	}
	return entries, nil
}

func (s *Service) validateTacticsDeck(request []byte) error {
	entries, err := decodeTacticsDeck(request)
	if err != nil {
		return err
	}
	// The request only carries seq and deck_info. The client normally enters
	// the stage before saving; bind the saved party to that server context.
	if s.state.BattleUID != 0 {
		event, err := s.resolve(s.state.BattleUID, 20)
		if err != nil {
			return err
		}
		group, ok := s.design.Row("TacticsBingoGroupTable", 3, event.ID)
		stage, found := s.row("TacticsBingoTable", 4, 5, group.V(1), s.state.BattleStage)
		if !ok || !found || stage.V(2) != s.state.BattleDeck {
			return errors.New("eventactions: tactics battle context missing")
		}
		party, err := s.tacticsBlueParty(stage.V(1))
		if err != nil {
			return err
		}
		if matchesTacticsParty(entries, party) {
			return nil
		}
		return errors.New("eventactions: tactics deck does not match entered stage")
	}
	// Without a bound stage, accept only a complete authored party belonging
	// to a currently active tactics schedule, never an arbitrary mixed roster.
	for _, event := range s.registry.List() {
		if event.Type != 20 || !s.active(event) {
			continue
		}
		group, ok := s.design.Row("TacticsBingoGroupTable", 3, event.ID)
		if !ok {
			continue
		}
		for _, stage := range s.design.Tables["TacticsBingoTable"] {
			if stage.V(4) != group.V(1) {
				continue
			}
			party, err := s.tacticsBlueParty(stage.V(1))
			if err != nil {
				return err
			}
			if matchesTacticsParty(entries, party) {
				return nil
			}
		}
	}
	return errors.New("eventactions: tactics deck has no active authored party")
}

func val(p []byte, n int) uint64 { v, _, _ := wire.Varint(p, n); return v }

func (s *Service) Handle(ctx command.Context, path string, b []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/FieldEventSpawnInfo": 551, "/FieldEventSpawnStart": 552, "/FieldEventSpawnReward": 553, "/FireWorksInfo": 554, "/FireWorksReward": 555, "/CharVoteInfo": 581, "/CharVoteSave": 582, "/CharVoteRanking": 583, "/CharVoteSeasonRanking": 584, "/CharVoteFavoriteAdd": 585, "/CharVoteFavoriteDelete": 586, "/CharVoteTotalRanking": 587, "/FriendshipSpecialEpisodeInfo": 623, "/FriendshipSpecialEpisodeClear": 624, "/ChargeCostInfo": 123, "/NpcQuizInfo": 535, "/NpcQuizClear": 536, "/TacticsBingoInfo": 546, "/TacticsBingoDeckSave": 550}
	code, ok := codes[path]
	if path == "/CafeteriaEventNpcInteractionReward" {
		code, ok = 414, true
	}
	if path == "/DailyStoryInfo" {
		code, ok = 538, true
	}
	if path == "/DailyStoryClear" {
		code, ok = 539, true
	}
	if !ok {
		return 0, nil, false, nil
	}

	seq, found, e := wire.Varint(b, 1)
	if e != nil || !found || seq == 0 {
		return code, nil, true, errors.New("eventactions: invalid request")
	}
	if e = wire.Walk(b, func(wire.Field) error { return nil }); e != nil {
		return code, nil, true, e
	}
	sum := sha256.Sum256(append([]byte(path), b...))
	digest := hex.EncodeToString(sum[:])
	rk := ctx.SessionID + ":" + path + ":" + key(seq)
	if r, ok := s.state.Receipts[rk]; ok {
		if r.Digest != digest {
			return code, nil, true, errors.New("eventactions: replay changed")
		}
		return code, r.Reply, true, nil
	}
	before, _ := json.Marshal(s.state)
	s.roll()
	out, e := s.handle(ctx, path, b, rk)
	if e != nil {
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, e
	}
	s.state.Receipts[rk] = receipt{digest, out}
	if e = s.save(ctx); e != nil {
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, e
	}
	return code, out, true, nil
}

func (s *Service) spawnWire(p *spawn) []byte {
	if p == nil {
		return nil
	}
	b := wire.AppendVarint(nil, 1, uint64(p.Start))
	b = wire.AppendVarint(b, 2, p.UID)
	b = wire.AppendVarint(b, 3, p.ID)
	b = wire.AppendVarint(b, 4, p.Group)
	var ids []string
	for k := range p.Caught {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, k := range ids {
		var g, id uint64
		_, _ = fmt.Sscanf(k, "%d/%d", &g, &id)
		x := wire.AppendVarint(nil, 1, p.ID)
		x = wire.AppendVarint(x, 2, g)
		x = wire.AppendVarint(x, 3, id)
		b = wire.AppendBytes(b, 5, x)
	}
	return b
}

func (s *Service) handle(ctx command.Context, path string, b []byte, identity string) ([]byte, error) {
	switch path {
	case "/DailyStoryInfo", "/DailyStoryClear":
		return s.dailyStory(ctx, path, b, identity)
	case "/CafeteriaEventNpcInteractionReward":
		return s.cafeteriaReward(ctx, b, identity)
	case "/ChargeCostInfo":
		if s.chargeInfo == nil {
			return nil, errors.New("eventactions: charge state source unavailable")
		}
		return s.chargeInfo(ctx)
	case "/FieldEventSpawnInfo":
		uid := val(b, 2)
		if _, e := s.resolve(uid, 21); e != nil {
			return nil, e
		}
		var out []byte
		if p := s.state.Spawns[uid]; p != nil {
			out = wire.AppendBytes(out, 1, s.spawnWire(p))
		}
		out = wire.AppendVarint(out, 2, s.state.DailyNormal)
		out = wire.AppendVarint(out, 3, s.state.DailySpecial)
		return out, nil
	case "/FieldEventSpawnStart":
		v, e := s.resolve(val(b, 2), 21)
		if e != nil {
			return nil, e
		}
		g, id := val(b, 3), val(b, 4)
		r, ok := s.row("FieldSpawnEventTable", 4, 5, g, id)
		if !ok || g != v.ID {
			return nil, errors.New("eventactions: invalid spawn event")
		}
		if err := s.spawnWindow(r); err != nil {
			return nil, err
		}
		if p := s.state.Spawns[v.UID]; p != nil {
			defaults, _ := s.design.Row("FieldEventDefaultTable", 9, 0)
			expired := defaults.V(12) > 0 && s.now().UnixMilli()-p.Start >= int64(defaults.V(12))*1000
			if expired && (p.Group != g || p.ID != id) {
				delete(s.state.Spawns, v.UID)
			} else {
				if p.Group != g || p.ID != id {
					return nil, errors.New("eventactions: spawn already started")
				}
				return wire.AppendBytes(nil, 1, s.spawnWire(p)), nil
			}
		}
		_ = r
		s.state.Spawns[v.UID] = &spawn{v.UID, g, id, s.now().UnixMilli(), map[string]bool{}}
		return wire.AppendBytes(nil, 1, s.spawnWire(s.state.Spawns[v.UID])), nil
	case "/FieldEventSpawnReward":
		v, e := s.resolve(val(b, 6), 21)
		if e != nil {
			return nil, e
		}
		p := s.state.Spawns[v.UID]
		if p == nil || p.ID != val(b, 2) || p.Group != val(b, 4) {
			return nil, errors.New("eventactions: spawn not started")
		}
		spawnRow, ok := s.row("FieldSpawnEventTable", 4, 5, p.Group, p.ID)
		if !ok {
			return nil, errors.New("eventactions: spawn definition absent")
		}
		g, id := val(b, 5), val(b, 3)
		r, ok := s.row("FieldEventMonsterTable", 3, 4, g, id)
		if !ok || g != spawnRow.V(2) {
			return nil, errors.New("eventactions: invalid spawn monster")
		}
		ck := key(g, id)
		if p.Caught[ck] {
			// A new transport sequence must not turn a confirmed catch into
			// either another grant or an error/recovery loop.
			return wire.AppendBytes(nil, 1, nil), nil
		}
		defaults, _ := s.design.Row("FieldEventDefaultTable", 9, 0)
		if defaults.V(12) > 0 && s.now().UnixMilli()-p.Start >= int64(defaults.V(12))*1000 {
			return nil, errors.New("eventactions: spawn time expired")
		}
		special := r.V(2) != 0
		limited := special && s.state.DailySpecial >= defaults.V(5) || !special && s.state.DailyNormal >= defaults.V(4)
		if limited {
			// Participation remains available after the daily reward quota.
			// Completing the capture without a grant also prevents client retries.
			p.Caught[ck] = true
			return wire.AppendBytes(nil, 1, nil), nil
		}
		reward, ok := s.design.SpawnRewards[[2]uint64{r.V(5), r.V(1)}]
		if !ok || reward.Count == 0 {
			return nil, errors.New("eventactions: spawn reward missing")
		}
		bundle, e := s.economy.Apply(ctx, identity, nil, []gamedata.Reward{reward})
		if e != nil {
			return nil, e
		}
		p.Caught[ck] = true
		if special {
			s.state.DailySpecial++
		} else {
			s.state.DailyNormal++
		}
		if s.progress != nil {
			if e = s.progress(ctx, 349, r.V(2), 1); e != nil {
				return nil, e
			}
		}
		return wire.AppendBytes(nil, 1, bundle), nil
	case "/FireWorksInfo":
		var out []byte
		for k := range s.state.Claims {
			var uid, g uint64
			if _, e := fmt.Sscanf(k, "fire:%d/%d", &uid, &g); e == nil {
				out = wire.AppendVarint(out, 1, g)
			}
		}
		return out, nil
	case "/FireWorksReward":
		v, e := s.resolve(val(b, 2), 22)
		if e != nil {
			return nil, e
		}
		g := val(b, 3)
		if g != v.ID {
			return nil, errors.New("eventactions: wrong fireworks group")
		}
		ck := "fire:" + key(v.UID, g)
		if s.state.Claims[ck] {
			return nil, errors.New("eventactions: fireworks already received")
		}
		var rs []gamedata.Reward
		for _, r := range s.design.Tables["FireworksTable"] {
			if r.V(4) == g {
				rs = append(rs, r.Rewards...)
				break
			}
		}
		if len(rs) == 0 {
			return nil, errors.New("eventactions: fireworks design missing")
		}
		bundle, e := s.economy.Apply(ctx, identity, nil, rs)
		if e != nil {
			return nil, e
		}
		s.state.Claims[ck] = true
		return wire.AppendBytes(nil, 1, bundle), nil
	case "/FriendshipSpecialEpisodeInfo":
		var out []byte
		for k := range s.state.Claims {
			var g, id uint64
			if _, e := fmt.Sscanf(k, "friend:%d/%d", &g, &id); e == nil {
				x := wire.AppendVarint(nil, 1, g)
				x = wire.AppendVarint(x, 2, id)
				out = wire.AppendBytes(out, 1, x)
			}
		}
		return out, nil
	case "/FriendshipSpecialEpisodeClear":
		g, id := val(b, 2), val(b, 3)
		r, ok := s.row("FriendshipSpecialEpisodeTable", 5, 6, g, id)
		if !ok {
			return nil, errors.New("eventactions: unknown episode")
		}
		if s.friendship == nil || s.friendship(g) < r.V(12) {
			return nil, errors.New("eventactions: friendship level insufficient")
		}
		if id > 1 && !s.state.Claims["friend:"+key(g, id-1)] {
			return nil, errors.New("eventactions: previous episode incomplete")
		}
		ck := "friend:" + key(g, id)
		if s.state.Claims[ck] {
			return nil, errors.New("eventactions: episode already cleared")
		}
		rs := append([]gamedata.Reward(nil), r.Rewards...)
		for _, v := range s.registry.List() {
			if v.Type == 24 && v.ID == g && s.active(v) {
				rs = append(rs, r.EventRewards...)
				break
			}
		}
		bundle, e := s.economy.Apply(ctx, identity, nil, rs)
		if e != nil {
			return nil, e
		}
		s.state.Claims[ck] = true
		x := wire.AppendVarint(nil, 1, g)
		x = wire.AppendVarint(x, 2, id)
		out := wire.AppendBytes(nil, 1, bundle)
		out = wire.AppendBytes(out, 2, x)
		return out, nil
	case "/NpcQuizInfo":
		uid := val(b, 2)
		v, e := s.resolveQuiz(ctx, uid)
		if e != nil {
			return nil, e
		}
		var out []byte
		for _, r := range s.design.Tables["NpcQuizTable"] {
			if r.V(1) != v.ID {
				continue
			}
			if s.state.Claims["quiz:"+key(uid, r.V(1), r.V(2))] {
				x := wire.AppendVarint(nil, 1, uid)
				x = wire.AppendVarint(x, 2, r.V(1))
				x = wire.AppendVarint(x, 3, r.V(2))
				out = wire.AppendBytes(out, 1, x)
			}
		}
		return out, nil
	case "/NpcQuizClear":
		uid, g, id := val(b, 2), val(b, 3), val(b, 4)
		v, e := s.resolveQuiz(ctx, uid)
		if e != nil {
			return nil, e
		}
		if !s.active(v) {
			return nil, errors.New("eventactions: quiz event inactive")
		}
		r, ok := s.row("NpcQuizTable", 1, 2, g, id)
		if !ok || g != v.ID || s.now().UnixMilli() < v.Start+int64(r.V(8))*86400000 {
			return nil, errors.New("eventactions: quiz locked")
		}
		ck := "quiz:" + key(uid, g, id)
		if s.state.Claims[ck] {
			return nil, errors.New("eventactions: quiz already cleared")
		}
		bundle, e := s.economy.Apply(ctx, identity, nil, r.Rewards)
		if e != nil {
			return nil, e
		}
		s.state.Claims[ck] = true
		x := wire.AppendVarint(nil, 1, uid)
		x = wire.AppendVarint(x, 2, g)
		x = wire.AppendVarint(x, 3, id)
		out := wire.AppendBytes(nil, 1, bundle)
		return wire.AppendBytes(out, 2, x), nil
	case "/TacticsBingoInfo":
		uid := val(b, 2)
		v, e := s.registry.Resolve(uid)
		if e != nil {
			return nil, e
		}
		if v.Type != 20 {
			return nil, errors.New("eventactions: wrong tactics event")
		}
		out := wire.AppendVarint(nil, 1, uid)
		out = wire.AppendVarint(out, 2, v.ID)
		for _, id := range s.state.Tactics[uid] {
			out = wire.AppendVarint(out, 3, id)
		}
		if !s.active(v) {
			out = wire.AppendVarint(out, 4, 1)
		}
		return out, nil
	case "/TacticsBingoDeckSave":
		if err := s.validateTacticsDeck(b); err != nil {
			return nil, err
		}
		s.state.Deck = append([]byte(nil), b...)
		return nil, nil
	default:
		return s.voteHandle(ctx, path, b, identity)
	}
}

func (s *Service) dailyStory(ctx command.Context, path string, b []byte, identity string) ([]byte, error) {
	if path == "/DailyStoryInfo" {
		var ids []uint64
		for k, claimed := range s.state.Claims {
			if claimed && strings.HasPrefix(k, "daily-story:") {
				id, err := strconv.ParseUint(strings.TrimPrefix(k, "daily-story:"), 10, 64)
				if err == nil {
					ids = append(ids, id)
				}
			}
		}
		slices.Sort(ids)
		var out []byte
		for _, id := range ids {
			out = wire.AppendVarint(out, 1, id)
		}
		return out, nil
	}
	id := val(b, 2)
	if id == 0 || s.miniContent == nil || s.miniDesign == nil {
		return nil, errors.New("eventactions: daily story unavailable")
	}
	reward, ok := s.miniDesign.Stories[id]
	if !ok {
		return nil, errors.New("eventactions: unknown daily story")
	}
	ck := fmt.Sprintf("daily-story:%d", id)
	if s.state.Claims[ck] {
		return wire.AppendVarint(wire.AppendBytes(nil, 1, nil), 2, id), nil
	}
	routes, err := s.miniContent.ListMiniContentRoutes(ctx)
	if err != nil {
		return nil, err
	}
	available := false
	now := s.now().UnixMilli()
	for _, route := range routes {
		if route.ContentType != 14 || now < route.Start || now > route.End {
			continue
		}
		for _, story := range s.miniDesign.Groups[route.ContentID] {
			if story == id {
				available = true
			}
		}
	}
	if !available {
		return nil, errors.New("eventactions: daily story event inactive")
	}
	var rewards []gamedata.Reward
	if reward.Count > 0 {
		rewards = append(rewards, reward)
	}
	bundle, err := s.economy.Apply(ctx, identity, nil, rewards)
	if err != nil {
		return nil, err
	}
	s.state.Claims[ck] = true
	return wire.AppendVarint(wire.AppendBytes(nil, 1, bundle), 2, id), nil
}

func (s *Service) cafeteriaReward(ctx command.Context, req []byte, identity string) ([]byte, error) {
	group, id := val(req, 2), val(req, 3)
	row, ok := s.row("CafeteriaEventTable", 5, 6, group, id)
	if !ok || row.V(13) == 0 || row.V(11) == 0 {
		return nil, errors.New("eventactions: cafeteria interaction absent")
	}
	var uid uint64
	for _, v := range s.registry.List() {
		if v.Type == 25 && s.active(v) && (v.SubID == group || v.SubID == 0 && v.ID == group) {
			uid = v.UID
			break
		}
	}
	if uid == 0 {
		return nil, errors.New("eventactions: cafeteria event inactive")
	}
	if len(s.design.Tables["CafeteriaDefaultTable"]) != 1 {
		return nil, errors.New("eventactions: cafeteria default missing")
	}
	defaults := s.design.Tables["CafeteriaDefaultTable"][0]
	cap := defaults.V(8)
	if cap == 0 || s.state.CafeteriaCurrency >= cap {
		return nil, errors.New("eventactions: cafeteria daily currency limit")
	}
	term := defaults.V(3)
	if row.V(4) != 1 {
		term = defaults.V(28)
	}
	receiptKey := key(uid, group, id)
	now := s.now().UnixMilli()
	if last := s.state.CafeteriaLast[receiptKey]; last > 0 && now-last < int64(term)*1000 {
		return nil, errors.New("eventactions: cafeteria interaction cooldown")
	}
	count := min(row.V(11), cap-s.state.CafeteriaCurrency)
	rewards := []gamedata.Reward{{Type: row.V(13), ID: row.V(12), Count: count}}
	bundle, err := s.economy.Apply(ctx, identity, nil, rewards)
	if err != nil {
		return nil, err
	}
	s.state.CafeteriaCurrency += count
	s.state.CafeteriaLast[receiptKey] = now
	out := wire.AppendBytes(nil, 1, bundle)
	out = wire.AppendVarint(out, 2, s.state.CafeteriaCurrency)
	return out, nil
}

// Tactics uses the client's custom battle stage fields 8/9 and has fixed
// tutorial decks. Character health and ordinary pack rewards do not apply.
func (s *Service) HandlesBattle(mode uint64) bool { return mode == 29 }
func (s *Service) EnterBattle(ctx command.Context, req []byte, receipt string) ([]byte, error) {

	digest := battleDigest(req)
	if r, ok := s.state.Receipts[ctx.SessionID+":enter:"+receipt]; ok {
		if r.Digest != digest {
			return nil, errors.New("eventactions: changed battle enter replay")
		}
		return r.Reply, nil
	}
	before, _ := json.Marshal(s.state)
	if val(req, 5) != 29 {
		return nil, errors.New("eventactions: wrong tactics battle mode")
	}
	group, stage, deck := val(req, 8), val(req, 9), val(req, 4)
	row, ok := s.row("TacticsBingoTable", 4, 5, group, stage)
	if !ok || row.V(2) != deck {
		return nil, errors.New("eventactions: invalid tactics stage/deck")
	}
	var uid uint64
	for _, v := range s.registry.List() {
		if v.Type != 20 || !s.active(v) {
			continue
		}
		g, ok := s.design.Row("TacticsBingoGroupTable", 3, v.ID)
		if ok && g.V(1) == group {
			uid = v.UID
			break
		}
	}
	if uid == 0 {
		return nil, errors.New("eventactions: tactics event inactive")
	}
	if _, err := s.tacticsBlueParty(row.V(1)); err != nil {
		return nil, err
	}
	// The client sends BattleEnter before the new scene's DeckSave. Its
	// battle reset clears the prior stage's virtual party; do the same here.
	s.state.Deck = nil
	s.state.BattleUID, s.state.BattleStage, s.state.BattleDeck = uid, stage, deck
	s.state.Receipts[ctx.SessionID+":enter:"+receipt] = receiptRecord(nil, digest)
	if e := s.save(ctx); e != nil {
		_ = json.Unmarshal(before, &s.state)
		return nil, e
	}
	return nil, nil
}

func (s *Service) CompleteBattle(ctx command.Context, req []byte, receipt string) ([]byte, error) {

	if r, ok := s.state.Receipts[ctx.SessionID+":battle:"+receipt]; ok {
		if r.Digest != battleDigest(req) {
			return nil, errors.New("eventactions: changed battle completion replay")
		}
		return r.Reply, nil
	}
	before, _ := json.Marshal(s.state)
	uid, stage := s.state.BattleUID, s.state.BattleStage
	if uid == 0 || stage == 0 {
		return nil, errors.New("eventactions: tactics battle not entered")
	}
	if _, e := s.resolve(uid, 20); e != nil {
		return nil, e
	}
	if val(req, 2) == 1 && !contains(s.state.Tactics[uid], stage) {
		priorLines := s.tacticsLines(uid)
		s.state.Tactics[uid] = append(s.state.Tactics[uid], stage)
		if s.progress != nil {
			if e := s.progress(ctx, 346, stage, 1); e != nil {
				_ = json.Unmarshal(before, &s.state)
				return nil, e
			}
			lines := s.tacticsLines(uid)
			if lines > priorLines {
				if e := s.progress(ctx, 347, 0, lines-priorLines); e != nil {
					_ = json.Unmarshal(before, &s.state)
					return nil, e
				}
			}
			v, _ := s.registry.Resolve(uid)
			g, _ := s.design.Row("TacticsBingoGroupTable", 3, v.ID)
			n := uint64(5) - g.V(4)
			if uint64(len(s.state.Tactics[uid])) == n*n {
				if e := s.progress(ctx, 348, 0, 1); e != nil {
					_ = json.Unmarshal(before, &s.state)
					return nil, e
				}
			}
		}
	}
	var out []byte
	for _, id := range s.state.Tactics[uid] {
		out = wire.AppendVarint(out, 29, id)
	}
	s.state.BattleUID, s.state.BattleStage, s.state.BattleDeck = 0, 0, 0
	s.state.Receipts[ctx.SessionID+":battle:"+receipt] = receiptRecord(out, battleDigest(req))
	if e := s.save(ctx); e != nil {
		_ = json.Unmarshal(before, &s.state)
		return nil, e
	}
	return out, nil
}
