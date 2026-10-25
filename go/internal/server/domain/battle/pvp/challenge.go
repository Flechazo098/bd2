package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

func (s *Service) matching(ctx command.Context, mode Mode, request []byte) ([]byte, error) {
	season, e := s.activeSeason(mode)
	if e != nil {
		return nil, e
	}
	m := s.state.Modes[mode]
	if err := s.reconcile(); err != nil {
		return nil, err
	}
	if len(m.Pending) >= 256 {
		return nil, fmt.Errorf("pvp: pending settlements must be projected before another match")
	}
	if m.Active != nil && m.Active.Season == season.ID {
		return append([]byte(nil), m.Active.MatchReply...), nil
	}
	if len(m.Attack.Deck) == 0 {
		return nil, fmt.Errorf("pvp: save an attack formation before matching")
	}
	if e = s.validateDeck(mode, season.ID, m.Attack.Deck); e != nil {
		return nil, e
	}
	multiplier := uint64(1)
	if mode == Mirror {
		multiplier, e = scalar(request, 2)
		if e != nil {
			return nil, e
		}
		if multiplier == 0 {
			multiplier = 1
		}
		if multiplier > s.rules.Design.Mirror.BoostMaximum {
			return nil, fmt.Errorf("pvp: ticket boost exceeds maximum")
		}
	}
	p, e := s.progress(mode, season.ID)
	if e != nil {
		return nil, e
	}
	own, e := s.own(mode, season.ID, m.Attack, p)
	if e != nil {
		return nil, e
	}
	count := uint64(1)
	if mode == Colosseum {
		values, e := s.rules.Design.ColosseumRule(season.ID, 4)
		if e != nil || len(values) != 1 {
			return nil, fmt.Errorf("pvp: invalid matching count")
		}
		count = values[0]
	}
	if count == 0 || count > 100 {
		return nil, fmt.Errorf("pvp: unsupported matching count")
	}
	candidates, e := s.source.Candidates(CandidateQuery{Mode: mode, Season: season.ID, Owner: s.profile.OwnerID, VP: p.VP, Exclude: append([]string(nil), m.Recent...), Limit: int(count)})
	if e != nil {
		return nil, e
	}
	var accepted []PublishedSnapshot
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.Profile.OwnerID == "" || candidate.Profile.OwnerID == s.profile.OwnerID || candidate.Profile.OwnerIndex == 0 || candidate.Mode != mode || candidate.Season != season.ID || candidate.Version == 0 || seen[candidate.Profile.OwnerID] || candidate.VP > math.MaxInt32 {
			continue
		}
		deck, e := messages(candidate.Snapshot.FullDeck, 1)
		if e != nil || len(deck) == 0 || s.validateDeck(mode, season.ID, deck) != nil {
			continue
		}
		accepted = append(accepted, cloneSnapshot(candidate))
		seen[candidate.Profile.OwnerID] = true
		if len(accepted) == int(count) {
			break
		}
	}
	if len(accepted) == 0 {
		code := codes["/PvpBattleMatching"]
		if mode == Colosseum {
			code = codes["/ColosseumBattleMatching"]
		}
		return nil, &command.GameError{PacketCode: code, ErrorType: 2, Message: "pvp: no eligible published opponent"}
	}
	index, e := randomID()
	if e != nil {
		return nil, e
	}
	seed, e := randomID()
	if e != nil {
		return nil, e
	}
	c := &challenge{ID: fmt.Sprintf("%s:%s:%d", mode, s.profile.OwnerID, index), BattleIndex: index, Season: season.ID, Seed: seed & math.MaxInt32, FirstAttack: 1, Multiplier: multiplier, IssuedAt: uint64(s.now().UnixMilli()), Attacker: cloneSnapshot(own), Candidates: accepted}
	if mode == Mirror {
		c.Selected = accepted[0].Profile.OwnerIndex
		c.Started = false
	} else {
		values, e := s.rules.Design.ColosseumRule(season.ID, 13)
		if e != nil || len(values) != 1 {
			return nil, fmt.Errorf("pvp: invalid first attack rule")
		}
		if values[0] == 0 {
			c.FirstAttack = 1 + seed%2
		} else if values[0] <= 2 {
			c.FirstAttack = values[0]
		} else {
			return nil, fmt.Errorf("pvp: unsupported first attack rule")
		}
	}
	if e = s.spendTickets(ctx, mode, season.ID, "pvp:challenge:"+c.ID, multiplier); e != nil {
		return nil, e
	}
	if mode == Mirror {
		c.MatchReply = mirrorMatching(c)
	} else {
		c.MatchReply = colosseumMatching(c)
	}
	if s.preparation != nil {
		if err := s.preparation(ctx, mode, c.ID, cloneSnapshot(c.Attacker), append([]byte(nil), request...)); err != nil {
			return nil, err
		}
	}
	m.Active = c
	if e = s.save(ctx); e != nil {
		return nil, e
	}
	return append([]byte(nil), c.MatchReply...), nil
}

func (s *Service) start(ctx command.Context, mode Mode, request []byte) ([]byte, error) {
	season, e := s.activeSeason(mode)
	if e != nil {
		return nil, e
	}
	c := s.state.Modes[mode].Active
	if c == nil || c.Season != season.ID {
		return nil, fmt.Errorf("pvp: battle start has no issued challenge")
	}
	if mode == Mirror {
		seed, err := randomID()
		if err != nil {
			return nil, err
		}
		seed &= math.MaxInt32
		c.Seeds = append(c.Seeds, seed)
		c.Started = true
		if err = s.save(ctx); err != nil {
			return nil, err
		}
		return wire.AppendVarint(nil, 1, seed), nil
	}
	owner, e := scalar(request, 2)
	if e != nil || owner == 0 {
		return nil, fmt.Errorf("pvp: invalid selected opponent")
	}
	if c.Started && c.Selected != owner {
		return nil, fmt.Errorf("pvp: challenge opponent already selected")
	}
	var enemy *PublishedSnapshot
	for i := range c.Candidates {
		if c.Candidates[i].Profile.OwnerIndex == owner {
			enemy = &c.Candidates[i]
			break
		}
	}
	if enemy == nil {
		return nil, fmt.Errorf("pvp: selected opponent was not matched")
	}
	c.Selected, c.Started = owner, true
	if e = s.save(ctx); e != nil {
		return nil, e
	}
	buffs, _ := messages(c.Attacker.Snapshot.FullDeck, 5)
	out := addMessages(nil, 1, buffs)
	return wire.AppendBytes(out, 3, colosseumEnemy(*enemy, c.Seed)), nil
}

func applyDelta(v uint64, delta int64, minimum uint64) uint64 {
	n := int64(v) + delta
	if n < int64(minimum) {
		return minimum
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return uint64(n)
}
func (s *Service) finish(ctx command.Context, mode Mode, request []byte) ([]byte, error) {
	acceptedAt := uint64(s.now().UnixMilli())
	season, e := s.activeSeason(mode)
	if e != nil {
		return nil, e
	}
	m := s.state.Modes[mode]
	c := m.Active
	if c == nil || !c.Started || c.Season != season.ID || c.Selected == 0 {
		return nil, fmt.Errorf("pvp: battle result has no active challenge")
	}
	result, e := scalar(request, 2)
	if e != nil || result < 1 || result > 3 {
		return nil, fmt.Errorf("pvp: invalid battle result")
	}
	if mode == Mirror {
		survivors, e := ints(request, 3)
		if e != nil {
			return nil, e
		}
		ownDeck, _ := messages(c.Attacker.Snapshot.FullDeck, 1)
		valid := map[uint64]bool{}
		seen := map[uint64]bool{}
		for _, entry := range ownDeck {
			index, _ := scalar(entry, 1)
			valid[index] = true
		}
		for _, index := range survivors {
			if !valid[index] || seen[index] {
				return nil, fmt.Errorf("pvp: survivor is not a unique challenge character")
			}
			seen[index] = true
		}
	}
	var enemy PublishedSnapshot
	for _, v := range c.Candidates {
		if v.Profile.OwnerIndex == c.Selected {
			enemy = v
		}
	}
	if enemy.Profile.OwnerIndex == 0 {
		return nil, fmt.Errorf("pvp: frozen opponent missing")
	}
	change, e := s.rules.Policy.Resolve(ScoreInput{Mode: mode, Season: season.ID, AttackerVP: c.Attacker.VP, DefenderVP: enemy.VP, AttackerRank: c.Attacker.Rank, DefenderRank: enemy.Rank, Result: result, Multiplier: c.Multiplier})
	if e != nil {
		return nil, e
	}
	p, e := s.progress(mode, season.ID)
	if e != nil {
		return nil, e
	}
	minimum := s.rules.Design.Mirror.MinimumVP
	if mode == Colosseum {
		minimum = s.rules.Design.Colosseum.MinimumVP
	}
	before := p.VP
	p.VP = applyDelta(before, change.Attacker, minimum)
	change.Attacker = int64(p.VP) - int64(before)
	change.Defender = int64(applyDelta(enemy.VP, change.Defender, minimum)) - int64(enemy.VP)
	p.Attacks++
	switch result {
	case 1:
		p.Wins++
		p.DeckWins++
	case 2:
		p.Losses++
		p.DeckLosses++
	}
	var onceIDs []uint64
	if mode == Mirror {
		promotion, ids, e := s.promotionRewards(ctx, season.ID, p.VP, c.Attacker.Rank)
		if e != nil {
			return nil, e
		}
		change.Rewards = append(change.Rewards, promotion...)
		onceIDs = ids
	}
	bundle, e := s.grant(ctx, "pvp:result:"+c.ID, change.Rewards)
	if e != nil {
		return nil, e
	}
	settled := BattleSettled{Seeds: append([]uint64(nil), c.Seeds...), Version: 1, ChallengeID: c.ID, BattleIndex: c.BattleIndex, Mode: mode, Season: season.ID, Attacker: c.Attacker, Defender: enemy, Result: result, Multiplier: c.Multiplier, Seed: c.Seed, FirstAttack: c.FirstAttack, Timestamp: acceptedAt, AttackerChange: change.Attacker, DefenderChange: change.Defender}
	raw, e := json.Marshal(settled)
	if e != nil {
		return nil, e
	}
	if e = ctx.State.PutEntry("pvp", "battles", strconv.FormatUint(c.BattleIndex, 10), raw); e != nil {
		return nil, e
	}
	if e = publish(ctx, "pvp:result:"+c.ID, "pvp.battle.settled", settled); e != nil {
		return nil, e
	}
	m.ResultOrder++
	m.Pending["pvp:result:"+c.ID] = pendingResult{Season: season.ID, BattleIndex: c.BattleIndex, Order: m.ResultOrder, Delta: change.Attacker, Result: result}
	m.Progress[strconv.FormatUint(season.ID, 10)] = p
	m.Active = nil
	m.Recent = append([]string{enemy.Profile.OwnerID}, m.Recent...)
	limit := s.rules.Design.Mirror.MatchExclude
	if mode == Colosseum {
		limit = s.rules.Design.Colosseum.MatchExclude
	}
	if uint64(len(m.Recent)) > limit {
		m.Recent = m.Recent[:limit]
	}
	if e = s.save(ctx); e != nil {
		return nil, e
	}
	out := wire.AppendVarint(nil, 1, result)
	if mode == Mirror {
		items, e := messages(bundle, 1)
		if e != nil {
			return nil, e
		}
		out = addMessages(out, 2, items)
		out = wire.AppendVarint(out, 4, uint64(change.Attacker))
		out = appendInts(out, 7, onceIDs)
	} else {
		out = wire.AppendVarint(out, 2, uint64(change.Attacker))
		out = wire.AppendBytes(out, 5, bundle)
		rank, e := s.rank(mode, season.ID)
		if e != nil {
			return nil, e
		}
		out = appendDouble(out, 6, rank.TopPercent)
	}
	if s.completion != nil {
		extra, err := s.completion(ctx, mode, c.ID, result == 1, c.Attacker.Deck())
		if err != nil {
			return nil, err
		}
		if mode == Mirror {
			out = append(out, extra...)
		}
	}
	return out, nil
}

func (s *Service) claim(ctx command.Context, mode Mode) ([]byte, error) {
	entitlements, e := s.source.Entitlements(mode, s.profile.OwnerID, 32)
	if e != nil {
		return nil, e
	}
	for _, ent := range entitlements {
		if ent.ID == "" || ent.Mode != mode || ent.AttackCount == 0 {
			continue
		}
		_, claimed, e := ctx.State.LoadEntry("pvp", "season_claims", ent.ID)
		if e != nil {
			return nil, e
		}
		if claimed {
			continue
		}
		rewards := append([]gamedata.BattleReward(nil), ent.Rewards...)
		var onceIDs []uint64
		if mode == Mirror {
			promotion, ids, e := s.promotionRewards(ctx, ent.Season, ent.VP, ent.Rank)
			if e != nil {
				return nil, e
			}
			rewards = append(rewards, promotion...)
			onceIDs = ids
		}
		bundle, e := s.grant(ctx, "pvp:season:"+ent.ID, rewards)
		if e != nil {
			return nil, e
		}
		if e = ctx.State.PutEntry("pvp", "season_claims", ent.ID, []byte("true")); e != nil {
			return nil, e
		}
		if e = publish(ctx, "pvp:season:"+ent.ID, "pvp.season.claimed", SeasonClaimed{Version: 1, OwnerID: s.profile.OwnerID, EntitlementID: ent.ID, Mode: mode, Season: ent.Season}); e != nil {
			return nil, e
		}
		out := wire.AppendVarint(nil, 2, ent.VP)
		out = wire.AppendBytes(out, 3, bundle)
		out = wire.AppendVarint(out, 4, ent.Season)
		if mode == Mirror {
			out = wire.AppendVarint(out, 1, ent.Rank)
			out = appendInts(out, 5, onceIDs)
		} else {
			out = appendDouble(out, 1, ent.TopPercent)
		}
		return out, nil
	}
	return nil, fmt.Errorf("pvp: no unclaimed season entitlement")
}
