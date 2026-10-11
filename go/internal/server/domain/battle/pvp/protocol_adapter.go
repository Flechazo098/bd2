package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var codes = map[string]int{
	"/PvpBattleUserInfo": 86, "/PvpBattleDeckInfo": 87, "/PvpBattleDeckSave": 88, "/PvpBattleMatching": 89, "/PvpBattleStart": 90, "/PvpBattleEnd": 91, "/PvpBattleReward": 92, "/PvpBattleRanking": 93, "/PvpBattleRankUserDetail": 94, "/PvpBattleHistory": 97, "/PvpBattleHistoryDeckInfo": 98, "/PvpSeasonReward": 136, "/PvpBattleOnceRewardInfo": 277, "/PvpContentsItemRenew": 401,
	"/ColosseumUserInfo": 473, "/ColosseumSeasonReward": 474, "/ColosseumDeckInfo": 475, "/ColosseumDeckSave": 476, "/ColosseumContentsItemRenew": 478, "/ColosseumRanking": 479, "/ColosseumRankDetail": 480, "/ColosseumBattleHistory": 481, "/ColosseumBattleReplayInfo": 482, "/ColosseumBattleMatching": 483, "/ColosseumBattleStart": 484, "/ColosseumBattleEnd": 485, "/ColosseumBlessInfo": 486, "/ColosseumBlessSave": 487, "/ColosseumBattleCountReset": 488, "/ColosseumApBuy": 489,
	"/ColosseumPresetInfo": 493, "/ColosseumPresetSave": 494, "/ColosseumPresetDelete": 495, "/ColosseumPresetSlotAdd": 496, "/ColosseumPresetInfoChange": 497,
}

type receipt struct{ Request, Response []byte }

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	if ctx.AccountID != s.profile.OwnerID || ctx.State == nil {
		return code, nil, true, fmt.Errorf("pvp: player command ownership mismatch")
	}
	if err := s.refreshProfile(ctx); err != nil {
		return code, nil, true, err
	}
	if err := s.RefreshTickets(ctx); err != nil {
		return code, nil, true, err
	}
	seq, e := scalar(request, 1)
	if e != nil || seq == 0 || seq > math.MaxInt32 || len(request) > 1<<20 {
		return code, nil, true, fmt.Errorf("pvp: invalid request")
	}
	mode := Mirror
	if strings.HasPrefix(path, "/Colosseum") {
		mode = Colosseum
	}
	key := requestKey(ctx, path, seq)
	if raw, exists, e := ctx.State.LoadEntry("pvp", "replies", key); e != nil {
		return code, nil, true, e
	} else if exists {
		var r receipt
		if json.Unmarshal(raw, &r) != nil || !bytes.Equal(r.Request, request) {
			return code, nil, true, fmt.Errorf("pvp: request sequence reused with different payload")
		}
		return code, r.Response, true, nil
	}
	body, e := s.handle(ctx, mode, path, request)
	if e != nil {
		return code, nil, true, e
	}
	if mutates(path) {
		raw, e := json.Marshal(receipt{Request: request, Response: body})
		if e != nil {
			return code, nil, true, e
		}
		if e = ctx.State.PutEntry("pvp", "replies", key, raw); e != nil {
			return code, nil, true, e
		}
	}
	return code, body, true, nil
}
func mutates(path string) bool {
	for _, word := range []string{"Save", "Renew", "Matching", "Start", "End", "SeasonReward", "Reset", "ApBuy", "PresetDelete", "PresetSlotAdd", "PresetInfoChange"} {
		if strings.Contains(path, word) {
			return true
		}
	}
	return false
}

func (s *Service) handle(ctx command.Context, mode Mode, path string, request []byte) ([]byte, error) {
	if strings.HasPrefix(path, "/ColosseumPreset") {
		return s.preset(ctx, path, request)
	}
	m := s.state.Modes[mode]
	switch path {
	case "/PvpBattleUserInfo", "/ColosseumUserInfo":
		return s.userInfo(ctx, mode)
	case "/PvpBattleDeckInfo":
		out := addMessages(nil, 1, m.Attack.Deck)
		out = addMessages(out, 2, m.Attack.Snapshot.Contents)
		out = addMessages(out, 3, m.Defense.Deck)
		return addMessages(out, 4, m.Defense.Snapshot.Contents), nil
	case "/ColosseumDeckInfo":
		return addMessages(addMessages(nil, 1, m.Attack.Deck), 2, m.Attack.Snapshot.Contents), nil
	case "/PvpBattleDeckSave", "/ColosseumDeckSave":
		side := uint64(0)
		deckField, contentsField := 2, 3
		if mode == Mirror {
			var e error
			side, e = scalar(request, 2)
			if e != nil {
				return nil, e
			}
			deckField, contentsField = 3, 4
		}
		deck, e := messages(request, deckField)
		if e != nil {
			return nil, e
		}
		contents, e := messages(request, contentsField)
		if e != nil {
			return nil, e
		}
		return nil, s.saveFormation(ctx, mode, side, deck, contents, false)
	case "/PvpContentsItemRenew", "/ColosseumContentsItemRenew":
		side := uint64(0)
		if mode == Mirror {
			var e error
			side, e = scalar(request, 2)
			if e != nil {
				return nil, e
			}
		}
		if e := s.saveFormation(ctx, mode, side, nil, nil, true); e != nil {
			return nil, e
		}
		f := m.Attack
		if side == 1 {
			f = m.Defense
		}
		return addMessages(nil, 1, f.Snapshot.Contents), nil
	case "/PvpBattleMatching", "/ColosseumBattleMatching":
		return s.matching(ctx, mode, request)
	case "/PvpBattleStart", "/ColosseumBattleStart":
		return s.start(ctx, mode, request)
	case "/PvpBattleEnd", "/ColosseumBattleEnd":
		return s.finish(ctx, mode, request)
	case "/PvpSeasonReward", "/ColosseumSeasonReward":
		return s.claim(ctx, mode)
	case "/PvpBattleRanking", "/ColosseumRanking":
		return s.ranking(mode, request)
	case "/PvpBattleRankUserDetail", "/ColosseumRankDetail":
		return s.rankDetail(mode, request)
	case "/PvpBattleHistory", "/ColosseumBattleHistory":
		return s.history(ctx, mode)
	case "/PvpBattleHistoryDeckInfo", "/ColosseumBattleReplayInfo":
		return s.replay(ctx, mode, request)
	case "/ColosseumBlessInfo":
		out := appendInts(nil, 1, m.AttackBless)
		return appendInts(out, 2, m.DefenseBless), nil
	case "/ColosseumBlessSave":
		return nil, s.saveBless(ctx, request)
	case "/ColosseumBattleCountReset":
		season, e := s.current(mode)
		if e != nil {
			return nil, e
		}
		side, e := scalar(request, 2)
		if e != nil || side > 1 {
			return nil, fmt.Errorf("pvp: invalid statistics reset type")
		}
		p, e := s.progress(mode, season.ID)
		if e != nil {
			return nil, e
		}
		now := uint64(s.now().UnixMilli())
		if side == 0 {
			p.DeckWins, p.DeckLosses, p.AttackReset = 0, 0, now
			p.AttackBaseWins, p.AttackBaseLosses = p.Wins, p.Losses
		} else {
			p.DeckDefenseWins, p.DeckDefenseLosses, p.DefenseReset = 0, 0, now
			p.DefenseBaseWins, p.DefenseBaseLosses = p.DefenseWins, p.DefenseLosses
		}
		m.Progress[strconv.FormatUint(season.ID, 10)] = p
		if e = s.save(ctx); e != nil {
			return nil, e
		}
		return wire.AppendVarint(wire.AppendVarint(nil, 1, p.AttackReset), 2, p.DefenseReset), nil
	case "/ColosseumApBuy":
		return s.buyTicket(ctx)
	case "/PvpBattleOnceRewardInfo":
		season, e := s.current(Mirror)
		if e != nil {
			return nil, e
		}
		ids, e := s.onceRewardIDs(ctx, season.ID)
		return appendInts(nil, 1, ids), e
	case "/PvpBattleReward":
		return nil, nil
	}
	return nil, fmt.Errorf("pvp: unsupported command")
}

func appendInts(out []byte, field int, values []uint64) []byte {
	for _, v := range values {
		out = wire.AppendVarint(out, field, v)
	}
	return out
}
func appendDouble(out []byte, field int, value float64) []byte {
	out = binary.AppendUvarint(out, uint64(field<<3|1))
	return binary.LittleEndian.AppendUint64(out, math.Float64bits(value))
}
func baseInfo(mode Mode, vp, rank, wins, losses uint64, percent float64) []byte {
	out := wire.AppendVarint(nil, 1, vp)
	out = wire.AppendVarint(out, 2, rank)
	out = wire.AppendVarint(out, 3, wins)
	out = wire.AppendVarint(out, 4, losses)
	if mode == Colosseum {
		out = appendDouble(out, 5, percent)
	}
	return out
}
func (s *Service) userInfo(ctx command.Context, mode Mode) ([]byte, error) {
	season, e := s.current(mode)
	if e != nil {
		return nil, e
	}
	p, e := s.progress(mode, season.ID)
	if e != nil {
		return nil, e
	}
	rank, e := s.rank(mode, season.ID)
	if e != nil {
		return nil, e
	}
	ent, e := s.source.Entitlements(mode, s.profile.OwnerID, 32)
	if e != nil {
		return nil, e
	}
	reward := uint64(0)
	for _, v := range ent {
		if v.ID == "" || v.Mode != mode || v.AttackCount == 0 {
			continue
		}
		_, exists, e := ctx.State.LoadEntry("pvp", "season_claims", v.ID)
		if e != nil {
			return nil, e
		}
		if !exists {
			reward = 1
			break
		}
	}
	base := baseInfo(mode, p.VP, rank.Rank, p.Wins+p.DefenseWins, p.Losses+p.DefenseLosses, rank.TopPercent)
	if mode == Mirror {
		out := wire.AppendBytes(nil, 1, base)
		out = wire.AppendVarint(out, 2, reward)
		return wire.AppendVarint(out, 3, season.ID), nil
	}
	out := wire.AppendVarint(nil, 1, season.ID)
	regular := uint64(0)
	for _, configured := range s.rules.Seasons {
		if configured.Mode == Colosseum && configured.Regular && (regular == 0 || configured.ID < regular) {
			regular = configured.ID
		}
	}
	rankVersion := season.ID
	group := s.rules.Design.ColosseumRankGroups[season.ID]
	for id, candidate := range s.rules.Design.ColosseumRankGroups {
		if candidate == group && id < rankVersion {
			rankVersion = id
		}
	}
	out = wire.AppendVarint(out, 2, regular)
	out = wire.AppendVarint(out, 3, rankVersion)
	out = wire.AppendBytes(out, 4, base)
	out = wire.AppendVarint(out, 5, reward)
	day := uint64(s.now().UnixMilli()) / 86400000
	count := s.state.Modes[mode].BuyCount
	if day != s.state.Modes[mode].BuyDay {
		count = 0
	}
	return wire.AppendVarint(out, 6, count), nil
}

func mirrorMatching(c *challenge) []byte {
	enemy := c.Candidates[0]
	p := enemy.Profile
	full := enemy.Snapshot.FullDeck
	out := wire.AppendVarint(nil, 1, c.Attacker.VP)
	out = wire.AppendVarint(out, 2, c.Attacker.Rank)
	buffs, _ := messages(c.Attacker.Snapshot.FullDeck, 5)
	out = addMessages(out, 3, buffs)
	out = wire.AppendVarint(out, 4, p.OwnerIndex)
	out = wire.AppendString(out, 5, p.Name)
	out = wire.AppendVarint(out, 6, p.XP)
	out = wire.AppendVarint(out, 7, enemy.VP)
	out = wire.AppendVarint(out, 8, enemy.Rank)
	if len(p.Guild) > 0 {
		out = wire.AppendBytes(out, 9, p.Guild)
	}
	out = wire.AppendVarint(out, 10, p.PortraitCostume)
	out = wire.AppendVarint(out, 11, p.PortraitDesign)
	for source, target := range map[int]int{1: 12, 2: 13, 3: 14, 4: 15, 5: 18, 6: 19} {
		values, _ := messages(full, source)
		out = addMessages(out, target, values)
	}
	return wire.AppendVarint(out, 16, c.Seed)
}
func colosseumEnemy(enemy PublishedSnapshot, seed uint64) []byte {
	p := enemy.Profile
	out := wire.AppendVarint(nil, 1, p.OwnerIndex)
	out = wire.AppendString(out, 2, p.Name)
	out = wire.AppendVarint(out, 3, p.XP)
	out = wire.AppendVarint(out, 4, enemy.VP)
	if len(p.Guild) > 0 {
		out = wire.AppendBytes(out, 6, p.Guild)
	}
	out = wire.AppendVarint(out, 7, p.PortraitCostume)
	out = wire.AppendVarint(out, 8, p.PortraitDesign)
	for source, target := range map[int]int{1: 9, 2: 10, 3: 11, 5: 12, 6: 13} {
		values, _ := messages(enemy.Snapshot.FullDeck, source)
		out = addMessages(out, target, values)
	}
	blesses, _ := ints(enemy.Snapshot.FullDeck, 8)
	out = appendInts(out, 14, blesses)
	out = wire.AppendVarint(out, 15, seed)
	return appendDouble(out, 16, enemy.TopPercent)
}
func colosseumMatching(c *challenge) []byte {
	out := wire.AppendVarint(nil, 1, c.Attacker.VP)
	out = wire.AppendVarint(out, 2, c.Attacker.Rank)
	for _, enemy := range c.Candidates {
		out = wire.AppendBytes(out, 3, colosseumEnemy(enemy, c.Seed))
	}
	return wire.AppendVarint(out, 4, c.FirstAttack)
}

func rankWire(mode Mode, r RankEntry) []byte {
	p := r.Profile
	out := wire.AppendVarint(nil, 1, p.OwnerIndex)
	out = wire.AppendString(out, 2, p.Name)
	out = wire.AppendVarint(out, 3, p.XP)
	out = wire.AppendVarint(out, 4, p.PortraitCostume)
	out = wire.AppendVarint(out, 5, p.PortraitDesign)
	if len(p.Guild) > 0 {
		out = wire.AppendBytes(out, 6, p.Guild)
	}
	out = wire.AppendBytes(out, 7, baseInfo(mode, r.VP, r.Rank, r.Wins, r.Losses, r.TopPercent))
	return wire.AppendVarint(out, 8, p.Title)
}
func (s *Service) ranking(mode Mode, request []byte) ([]byte, error) {
	season, e := scalar(request, 2)
	if e != nil {
		return nil, e
	}
	if season == 0 {
		c, e := s.current(mode)
		if e != nil {
			return nil, e
		}
		season = c.ID
	}
	rows, e := s.source.Ranking(RankQuery{Mode: mode, Season: season, Start: 0, Limit: 100})
	if e != nil {
		return nil, e
	}
	var out []byte
	for i, r := range rows {
		if i >= 100 {
			break
		}
		out = wire.AppendBytes(out, 1, rankWire(mode, r))
	}
	p, e := s.progress(mode, season)
	if e != nil {
		return nil, e
	}
	own, e := s.own(mode, season, s.state.Modes[mode].Defense, p)
	if e != nil {
		return nil, e
	}
	r := RankEntry{PublishedSnapshot: own, Wins: p.Wins + p.DefenseWins, Losses: p.Losses + p.DefenseLosses}
	return wire.AppendBytes(out, 2, rankWire(mode, r)), nil
}
func (s *Service) rankDetail(mode Mode, request []byte) ([]byte, error) {
	target, e := scalar(request, 2)
	if e != nil || target == 0 {
		return nil, fmt.Errorf("pvp: invalid detail owner")
	}
	season, e := s.current(mode)
	if e != nil {
		return nil, e
	}
	if target == s.profile.OwnerIndex {
		m := s.state.Modes[mode]
		p, e := s.progress(mode, season.ID)
		if e != nil {
			return nil, e
		}
		out := wire.AppendVarint(nil, 1, target)
		out = wire.AppendVarint(out, 2, p.Wins+p.DefenseWins)
		out = wire.AppendVarint(out, 3, p.Losses+p.DefenseLosses)
		out = wire.AppendBytes(out, 4, m.Attack.Snapshot.FullDeck)
		if mode == Mirror {
			out = wire.AppendBytes(out, 5, m.Defense.Snapshot.FullDeck)
		}
		return out, nil
	}
	rows, e := s.source.Ranking(RankQuery{Mode: mode, Season: season.ID, OwnerIndex: target, Limit: 1})
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		if r.Profile.OwnerIndex != target {
			continue
		}
		out := wire.AppendVarint(nil, 1, target)
		out = wire.AppendVarint(out, 2, r.Wins)
		out = wire.AppendVarint(out, 3, r.Losses)
		out = wire.AppendBytes(out, 4, r.Snapshot.FullDeck)
		if mode == Mirror {
			out = wire.AppendBytes(out, 5, r.Snapshot.FullDeck)
		}
		return out, nil
	}
	return nil, fmt.Errorf("pvp: published rank detail unavailable")
}

func (s *Service) saveBless(ctx command.Context, request []byte) error {
	season, e := s.current(Colosseum)
	if e != nil {
		return e
	}
	entries, e := messages(request, 2)
	if e != nil || len(entries) > 2 {
		return fmt.Errorf("pvp: invalid bless selection")
	}
	m := s.state.Modes[Colosseum]
	attack, defense := append([]uint64(nil), m.AttackBless...), append([]uint64(nil), m.DefenseBless...)
	sides := map[uint64]bool{}
	for _, entry := range entries {
		side, e := scalar(entry, 1)
		if e != nil || side > 1 || sides[side] {
			return fmt.Errorf("pvp: invalid bless deck")
		}
		sides[side] = true
		ids, e := ints(entry, 2)
		if e != nil {
			return e
		}
		kind := uint64(14)
		if side == 1 {
			kind = 15
		}
		budget, e := s.rules.Design.ColosseumRule(season.ID, kind)
		if e != nil || len(budget) != 1 {
			return fmt.Errorf("pvp: invalid bless budget")
		}
		banned, e := s.rules.Design.ColosseumRule(season.ID, 31)
		if e != nil {
			return e
		}
		seen := map[uint64]bool{}
		var total uint64
		for _, id := range ids {
			bless, ok := s.rules.Design.Blesses[id]
			if !ok || seen[id] || total > math.MaxInt32-bless.Points {
				return fmt.Errorf("pvp: invalid bless")
			}
			if slices.Contains(banned, id) {
				return fmt.Errorf("pvp: season bans this bless")
			}
			seen[id] = true
			total += bless.Points
		}
		if total > budget[0] {
			return fmt.Errorf("pvp: bless points exceed season budget")
		}
		if side == 0 {
			attack = ids
		} else {
			defense = ids
		}
	}
	m.AttackBless, m.DefenseBless = attack, defense
	if len(m.Attack.Deck) > 0 {
		return s.saveFormation(ctx, Colosseum, 0, m.Attack.Deck, m.Attack.Snapshot.Contents, false)
	}
	return s.save(ctx)
}
func (s *Service) buyTicket(ctx command.Context) ([]byte, error) {
	season, e := s.activeSeason(Colosseum)
	if e != nil {
		return nil, e
	}
	maximum, e := s.rules.Design.ColosseumRule(season.ID, 11)
	if e != nil || len(maximum) != 1 {
		return nil, fmt.Errorf("pvp: invalid ticket purchase maximum")
	}
	m := s.state.Modes[Colosseum]
	day := uint64(s.now().UnixMilli()) / 86400000
	if m.BuyDay != day {
		m.BuyDay, m.BuyCount = day, 0
	}
	if m.BuyCount >= maximum[0] {
		return nil, fmt.Errorf("pvp: daily ticket purchases exhausted")
	}
	d := s.rules.Design.Colosseum
	id := fmt.Sprintf("pvp:buy:%s:%d:%d", s.profile.OwnerID, day, m.BuyCount)
	if e = s.spend(ctx, id, []gamedata.BattleReward{{Type: d.TicketBuyType, ID: d.TicketBuyID, Count: d.TicketBuyCost}}); e != nil {
		return nil, e
	}
	amount, e := s.rules.Design.ColosseumRule(season.ID, 9)
	if e != nil || len(amount) != 1 || amount[0] == 0 {
		return nil, fmt.Errorf("pvp: no purchasable ticket grant")
	}
	bundle, e := s.grant(ctx, id+":grant", []gamedata.BattleReward{{Type: 52, Count: amount[0]}})
	if e != nil {
		return nil, e
	}
	m.BuyCount++
	if e = s.save(ctx); e != nil {
		return nil, e
	}
	return wire.AppendVarint(wire.AppendBytes(nil, 1, bundle), 2, m.BuyCount), nil
}

func (s *Service) history(ctx command.Context, mode Mode) ([]byte, error) {
	season, e := s.current(mode)
	if e != nil {
		return nil, e
	}
	limit, hours := s.rules.Design.Mirror.HistoryLimit, s.rules.Design.Mirror.HistoryHours
	if mode == Colosseum {
		limit, hours = s.rules.Design.Colosseum.HistoryLimit, s.rules.Design.Colosseum.HistoryHours
	}
	battles, e := s.source.History(HistoryQuery{Mode: mode, Season: season.ID, Owner: s.profile.OwnerID, Limit: int(limit * 2)})
	if e != nil {
		return nil, e
	}
	seen := map[uint64]bool{}
	for _, b := range battles {
		seen[b.BattleIndex] = true
	}
	for _, pending := range s.state.Modes[mode].Pending {
		if seen[pending.BattleIndex] {
			continue
		}
		raw, exists, e := ctx.State.LoadEntry("pvp", "battles", strconv.FormatUint(pending.BattleIndex, 10))
		if e != nil {
			return nil, e
		}
		if !exists {
			return nil, fmt.Errorf("pvp: pending battle receipt missing")
		}
		var b BattleSettled
		if e = json.Unmarshal(raw, &b); e != nil {
			return nil, e
		}
		if b.Mode == mode && b.Season == season.ID {
			battles = append(battles, b)
		}
	}
	sort.Slice(battles, func(i, j int) bool { return battles[i].Timestamp > battles[j].Timestamp })
	now := uint64(s.now().UnixMilli())
	var attackCount, defenseCount uint64
	var out []byte
	for _, b := range battles {
		if b.Timestamp > now || now-b.Timestamp > hours*3600000 {
			continue
		}
		defending := b.Defender.Profile.OwnerID == s.profile.OwnerID
		field := 1
		enemy := b.Defender
		change := b.AttackerChange
		result := b.Result
		if defending {
			field = 2
			enemy = b.Attacker
			change = b.DefenderChange
			switch result {
			case 1:
				result = 2
			case 2:
				result = 1
			}
			if defenseCount >= limit {
				continue
			}
			defenseCount++
		} else {
			if attackCount >= limit {
				continue
			}
			attackCount++
		}
		row := wire.AppendVarint(nil, 1, b.BattleIndex)
		row = wire.AppendVarint(row, 2, result)
		row = wire.AppendVarint(row, 3, enemy.Profile.OwnerIndex)
		row = wire.AppendString(row, 4, enemy.Profile.Name)
		row = wire.AppendVarint(row, 5, enemy.VP)
		row = wire.AppendVarint(row, 6, enemy.Rank)
		deck, _ := messages(enemy.Snapshot.FullDeck, 3)
		for _, costume := range deck {
			id, _ := scalar(costume, 2)
			row = wire.AppendVarint(row, 7, id)
		}
		row = wire.AppendVarint(row, 9, uint64(change))
		if mode == Mirror {
			row = wire.AppendVarint(row, 11, b.Timestamp)
		} else {
			row = wire.AppendVarint(row, 10, b.Timestamp)
			row = appendDouble(row, 11, enemy.TopPercent)
			ids, _ := ints(enemy.Snapshot.FullDeck, 8)
			row = appendInts(row, 13, ids)
		}
		out = wire.AppendBytes(out, field, row)
	}
	p, e := s.progress(mode, season.ID)
	if e != nil {
		return nil, e
	}
	var previous Season
	for _, configured := range s.rules.Seasons {
		if configured.Mode == mode && configured.End <= season.Start && configured.End > previous.End {
			previous = configured
		}
	}
	var prior progress
	if previous.ID != 0 {
		prior, e = s.progress(mode, previous.ID)
		if e != nil {
			return nil, e
		}
	}
	values := map[int]uint64{3: p.Wins + p.DefenseWins, 4: p.Losses + p.DefenseLosses, 5: p.Wins, 6: p.Losses, 7: p.DefenseWins, 8: p.DefenseLosses, 9: prior.Wins, 10: prior.Losses, 11: prior.DefenseWins, 12: prior.DefenseLosses, 13: p.DeckWins, 14: p.DeckLosses, 15: p.DeckDefenseWins, 16: p.DeckDefenseLosses, 17: p.AttackReset, 18: p.DefenseReset}
	for f, v := range values {
		out = wire.AppendVarint(out, f, v)
	}
	return out, nil
}
func (s *Service) replay(ctx command.Context, mode Mode, request []byte) ([]byte, error) {
	index, e := scalar(request, 2)
	if e != nil || index == 0 {
		return nil, fmt.Errorf("pvp: invalid battle index")
	}
	raw, ok, e := ctx.State.LoadEntry("pvp", "battles", strconv.FormatUint(index, 10))
	if e != nil {
		return nil, e
	}
	if !ok {
		rows, e := s.source.History(HistoryQuery{Mode: mode, Owner: s.profile.OwnerID, BattleIndex: index, Limit: 1})
		if e != nil {
			return nil, e
		}
		if len(rows) != 1 || rows[0].BattleIndex != index {
			return nil, fmt.Errorf("pvp: battle replay unavailable")
		}
		raw, e = json.Marshal(rows[0])
		if e != nil {
			return nil, e
		}
	}
	var b BattleSettled
	if json.Unmarshal(raw, &b) != nil || b.Mode != mode || b.Attacker.Profile.OwnerID != s.profile.OwnerID && b.Defender.Profile.OwnerID != s.profile.OwnerID {
		return nil, fmt.Errorf("pvp: replay ownership mismatch")
	}
	blue, red := b.Attacker, b.Defender
	if mode == Mirror && b.Defender.Profile.OwnerID == s.profile.OwnerID {
		blue, red = red, blue
	}
	out := wire.AppendBytes(nil, 1, blue.Snapshot.FullDeck)
	out = wire.AppendBytes(out, 2, red.Snapshot.FullDeck)
	if mode == Colosseum {
		out = wire.AppendVarint(out, 3, b.Seed)
		out = wire.AppendVarint(out, 5, b.FirstAttack)
	}
	return out, nil
}
