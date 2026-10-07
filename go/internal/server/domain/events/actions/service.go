// Package eventactions owns repeatable event interaction state. It uses the
// injected calendar and shared transactional reward service.
package eventactions

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"time"
)

type Economy interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
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
	store    stateio.Store
	design   *gamedata.EventActionsDesign
	registry events.Resolver
	economy  Economy
	state    snapshot
	now      func() time.Time

	friendship  func(uint64) uint64
	chargeInfo  func(ctx command.Context) ([]byte, error)
	progress    func(ctx command.Context, _ uint64, _ uint64, _ uint64) error
	voteTotals  func(uint64, uint64) (map[uint64]uint64, error)
	miniContent MiniContentResolver
	miniDesign  *gamedata.MiniContentDesign
}

func Open(ctx command.Context, store stateio.Store, d *gamedata.EventActionsDesign, r events.Resolver, e Economy) (*Service, error) {
	if store == nil || d == nil || r == nil || e == nil {
		return nil, errors.New("eventactions: missing dependency")
	}
	s := &Service{store: store, design: d, registry: r, economy: e, now: time.Now}
	b, err := store.Load(ctx.State, "eventactions")
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

func (s *Service) AttachFriendshipLevel(f func(uint64) uint64)                  { s.friendship = f }
func (s *Service) AttachChargeInfo(f func(ctx command.Context) ([]byte, error)) { s.chargeInfo = f }
func (s *Service) AttachProgress(f func(ctx command.Context, _ uint64, _ uint64, _ uint64) error) {
	s.progress = f
}

// The current server owns one account save. Its real votes are the default
// leaderboard; a future account aggregator can supply broader server totals.
func (s *Service) AttachVoteTotals(f func(uint64, uint64) (map[uint64]uint64, error)) {
	s.voteTotals = f
}
func (s *Service) save(ctx command.Context) error {
	b, e := json.Marshal(s.state)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "eventactions", b)
}

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

func (s *Service) row(table string, groupField, idField int, g, id uint64) (gamedata.EventActionRow, bool) {
	for _, r := range s.design.Tables[table] {
		if r.V(groupField) == g && r.V(idField) == id {
			return r, true
		}
	}
	return gamedata.EventActionRow{}, false
}

func (s *Service) RecordTacticsClear(ctx command.Context, uid, stage uint64) error {

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
	return s.save(ctx)
}
