// Package eventactions owns repeatable event interaction state. It uses the
// injected calendar and shared transactional reward service.
package eventactions

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type Economy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type receipt struct {
	Digest string
	Reply  []byte
}
type spawn struct {
	UID, Group, ID uint64
	Start          int64
	Caught         map[string]bool
}
type vote struct{ Round, Candidate, Normal, Additional uint64 }
type snapshot struct {
	Version                            string
	BattleUID, BattleStage, BattleDeck uint64
	Claims                             map[string]bool
	Favorites                          map[uint64]bool
	Votes                              map[string]*vote
	NormalVoted                        map[string]bool
	VoteRewards                        map[string]bool
	Spawns                             map[uint64]*spawn
	DailyNormal, DailySpecial          uint64
	CafeteriaCurrency                  uint64
	CafeteriaLast                      map[string]int64
	Day                                string
	Tactics                            map[uint64][]uint64
	Deck                               []byte
	Receipts                           map[string]receipt
}
type Service struct {
	mu          sync.Mutex
	store       stateio.Store
	design      *gamedata.EventActionsDesign
	registry    events.Resolver
	economy     Economy
	state       snapshot
	now         func() time.Time
	session     string
	friendship  func(uint64) uint64
	chargeInfo  func() ([]byte, error)
	progress    func(uint64, uint64, uint64) error
	voteTotals  func(uint64, uint64) (map[uint64]uint64, error)
	miniContent MiniContentResolver
	miniDesign  *gamedata.MiniContentDesign
}

func Open(store stateio.Store, d *gamedata.EventActionsDesign, r events.Resolver, e Economy) (*Service, error) {
	if store == nil || d == nil || r == nil || e == nil {
		return nil, errors.New("eventactions: missing dependency")
	}
	s := &Service{store: store, design: d, registry: r, economy: e, now: time.Now}
	b, err := store.Load("eventactions")
	if err != nil {
		return nil, err
	}
	if b != nil {
		if err = stateio.RequireExactJSONObject(b, "Version", "BattleUID", "BattleStage", "BattleDeck", "Claims", "Favorites", "Votes", "NormalVoted", "VoteRewards", "Spawns", "DailyNormal", "DailySpecial", "CafeteriaCurrency", "CafeteriaLast", "Day", "Tactics", "Deck", "Receipts"); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
		if s.state.Version != versionconfig.State() || s.state.Claims == nil || s.state.Favorites == nil || s.state.Votes == nil || s.state.NormalVoted == nil || s.state.VoteRewards == nil || s.state.Spawns == nil || s.state.Tactics == nil || s.state.Receipts == nil || s.state.CafeteriaLast == nil {
			return nil, errors.New("eventactions: invalid state layout")
		}
		for _, v := range s.state.Votes {
			if v == nil || v.Candidate == 0 || v.Round == 0 {
				return nil, errors.New("eventactions: invalid saved vote")
			}
		}
		for _, p := range s.state.Spawns {
			if p == nil || p.Caught == nil || p.ID == 0 {
				return nil, errors.New("eventactions: invalid saved spawn")
			}
		}
	} else {
		s.state.Version = versionconfig.State()
	}
	s.init()
	return s, nil
}
func (s *Service) init() {
	if s.state.CafeteriaLast == nil {
		s.state.CafeteriaLast = map[string]int64{}
	}
	if s.state.Claims == nil {
		s.state.Claims = map[string]bool{}
	}
	if s.state.Favorites == nil {
		s.state.Favorites = map[uint64]bool{}
	}
	if s.state.Votes == nil {
		s.state.Votes = map[string]*vote{}
	}
	if s.state.NormalVoted == nil {
		s.state.NormalVoted = map[string]bool{}
	}
	if s.state.VoteRewards == nil {
		s.state.VoteRewards = map[string]bool{}
	}
	if s.state.Spawns == nil {
		s.state.Spawns = map[uint64]*spawn{}
	}
	if s.state.Tactics == nil {
		s.state.Tactics = map[uint64][]uint64{}
	}
	if s.state.Receipts == nil {
		s.state.Receipts = map[string]receipt{}
	}
}
func (s *Service) BeginSession(id string)                              { s.mu.Lock(); defer s.mu.Unlock(); s.session = id }
func (s *Service) AttachFriendshipLevel(f func(uint64) uint64)         { s.friendship = f }
func (s *Service) AttachChargeInfo(f func() ([]byte, error))           { s.chargeInfo = f }
func (s *Service) AttachProgress(f func(uint64, uint64, uint64) error) { s.progress = f }

// The current server owns one account save. Its real votes are the default
// leaderboard; a future account aggregator can supply broader server totals.
func (s *Service) AttachVoteTotals(f func(uint64, uint64) (map[uint64]uint64, error)) {
	s.voteTotals = f
}
func (s *Service) save() error {
	b, e := json.Marshal(s.state)
	if e != nil {
		return e
	}
	return s.store.Save("eventactions", b)
}
func val(p []byte, n int) uint64 { v, _, _ := wire.Varint(p, n); return v }
func key(v ...uint64) string {
	r := []string{}
	for _, n := range v {
		r = append(r, fmt.Sprint(n))
	}
	return strings.Join(r, "/")
}
func (s *Service) active(v events.Schedule) bool {
	return s.now().UnixMilli() >= v.Start && s.now().UnixMilli() < v.End
}
func (s *Service) resolve(uid, t uint64) (events.Schedule, error) {
	v, e := s.registry.Resolve(uid)
	if e != nil {
		return v, e
	}
	if v.Type != t || !s.active(v) {
		return v, errors.New("eventactions: event inactive or wrong type")
	}
	return v, nil
}
func (s *Service) find(t uint64) (events.Schedule, bool) {
	for _, v := range s.registry.List() {
		if v.Type == t && s.active(v) {
			return v, true
		}
	}
	return events.Schedule{}, false
}
func (s *Service) roll() {
	day := s.now().UTC().Format("2006-01-02")
	if s.state.Day != day {
		s.state.Day = day
		s.state.DailyNormal = 0
		s.state.DailySpecial = 0
		s.state.Spawns = map[uint64]*spawn{}
		s.state.CafeteriaCurrency = 0
		s.state.CafeteriaLast = map[string]int64{}
		s.state.NormalVoted = map[string]bool{}
	}
}
func (s *Service) Handle(path string, b []byte) (int, []byte, bool, error) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, found, e := wire.Varint(b, 1)
	if e != nil || !found || seq == 0 {
		return code, nil, true, errors.New("eventactions: invalid request")
	}
	if e = wire.Walk(b, func(wire.Field) error { return nil }); e != nil {
		return code, nil, true, e
	}
	sum := sha256.Sum256(append([]byte(path), b...))
	digest := hex.EncodeToString(sum[:])
	rk := s.session + ":" + path + ":" + key(seq)
	if r, ok := s.state.Receipts[rk]; ok {
		if r.Digest != digest {
			return code, nil, true, errors.New("eventactions: replay changed")
		}
		return code, r.Reply, true, nil
	}
	before, _ := json.Marshal(s.state)
	s.roll()
	out, e := s.handle(path, b, rk)
	if e != nil {
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, e
	}
	s.state.Receipts[rk] = receipt{digest, out}
	if e = s.save(); e != nil {
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, e
	}
	return code, out, true, nil
}
func (s *Service) row(table string, groupField, idField int, g, id uint64) (gamedata.EventActionRow, bool) {
	for _, r := range s.design.Tables[table] {
		if r.V(groupField) == g && r.V(idField) == id {
			return r, true
		}
	}
	return gamedata.EventActionRow{}, false
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
func (s *Service) handle(path string, b []byte, identity string) ([]byte, error) {
	switch path {
	case "/DailyStoryInfo", "/DailyStoryClear":
		return s.dailyStory(path, b, identity)
	case "/CafeteriaEventNpcInteractionReward":
		return s.cafeteriaReward(b, identity)
	case "/ChargeCostInfo":
		if s.chargeInfo == nil {
			return nil, errors.New("eventactions: charge state source unavailable")
		}
		return s.chargeInfo()
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
		bundle, e := s.economy.Apply(identity, nil, []gamedata.Reward{reward})
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
			if e = s.progress(349, r.V(2), 1); e != nil {
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
		bundle, e := s.economy.Apply(identity, nil, rs)
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
		bundle, e := s.economy.Apply(identity, nil, rs)
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
		v, e := s.resolveQuiz(uid)
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
		v, e := s.resolveQuiz(uid)
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
		bundle, e := s.economy.Apply(identity, nil, r.Rewards)
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
		return s.voteHandle(path, b, identity)
	}
}

func (s *Service) RecordTacticsClear(uid, stage uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.resolve(uid, 20)
	if e != nil {
		return e
	}
	group, ok := s.design.Row("TacticsBingoGroupTable", 3, v.ID)
	if !ok {
		return errors.New("eventactions: tactics group missing")
	}
	if _, ok = s.row("TacticsBingoTable", 4, 5, group.V(1), stage); !ok {
		return errors.New("eventactions: invalid tactics stage")
	}
	if slices.Contains(s.state.Tactics[uid], stage) {
		return nil
	}
	s.state.Tactics[uid] = append(s.state.Tactics[uid], stage)
	slices.Sort(s.state.Tactics[uid])
	return s.save()
}
