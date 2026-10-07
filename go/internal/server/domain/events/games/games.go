// Package eventgames implements this server's persistent seasonal minigames.
package eventgames

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"

	"time"
)

type Resolver interface {
	Resolve(uint64) (events.Schedule, error)
}
type Rewards interface {
	ConsumeAndGrant(ctx command.Context, _ string, _ []assets.Item, _ []gamedata.BattleReward) ([]byte, error)
}
type GameState struct {
	UID, Type, ID, Position, Clear, Free, Reset, Tries, SinceSpecial uint64
	Board, Opened, Lines                                             []uint64
	Special                                                          bool
}
type reply struct{ Request, Body []byte }
type snapshot struct {
	Version int                  `json:"version"`
	Games   map[string]GameState `json:"games"`
	Replies map[string]reply     `json:"replies"`
}

// RuleLoader returns shared immutable game rules.
type RuleLoader func(uint64, uint64) (*gamedata.EventGame, error)

type Service struct {
	store    stateio.Store
	resolver Resolver
	rewards  Rewards
	state    snapshot
	load     func(uint64, uint64) (*gamedata.EventGame, error)
	sample   func(uint64) (uint64, error)
	now      func() time.Time
}

func Open(ctx command.Context, store stateio.Store, load RuleLoader, resolver Resolver, rewards Rewards) (*Service, error) {
	if store == nil || load == nil || resolver == nil || rewards == nil {
		return nil, fmt.Errorf("eventgames: incomplete runtime")
	}
	s := &Service{store: store, resolver: resolver, rewards: rewards, now: time.Now, state: snapshot{1, map[string]GameState{}, map[string]reply{}}}
	s.load = load
	s.sample = func(max uint64) (uint64, error) {
		if max == 0 {
			return 0, fmt.Errorf("eventgames: empty sample")
		}
		n, e := rand.Int(rand.Reader, new(big.Int).SetUint64(max))
		if e != nil {
			return 0, e
		}
		return n.Uint64(), nil
	}
	raw, e := store.Load(ctx.State, "eventgames")
	if e != nil {
		return nil, e
	}
	if raw != nil {
		if e = stateio.RequireExactJSONObject(raw, "version", "games", "replies"); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &s.state); e != nil {
			return nil, e
		}
		if s.state.Version != 1 || s.state.Games == nil || s.state.Replies == nil {
			return nil, fmt.Errorf("eventgames: incompatible state")
		}
	}
	for key, g := range s.state.Games {
		d, e := s.load(g.Type, g.ID)
		if e != nil {
			return nil, e
		}
		if e = validateSaved(key, g, d); e != nil {
			return nil, e
		}
	}
	return s, nil
}

func contains(a []uint64, v uint64) bool {
	return slices.Contains(a, v)
}

var routes = map[string]struct {
	Code int
	Kind uint64
}{"/MiniGameBoardInfo": {223, 12}, "/MiniGameBoardPlay": {224, 12}, "/MiniGameBingoInfo": {256, 13}, "/MiniGameBingoPlay": {257, 13}, "/MiniPuzzleInfo": {343, 17}, "/MiniPuzzleOpen": {344, 17}, "/MiniPuzzleAllOpen": {345, 17}, "/MiniPuzzleRenew": {346, 17}, "/MiniGameRouletteInfo": {415, 19}, "/MiniGameRouletteDraw": {416, 19}}

func (s *Service) initialize(g *GameState, d *gamedata.EventGame) error {
	if d.Type != 13 && d.Type != 17 {
		return nil
	}
	count := g.Clear
	if d.Type == 17 && count == 0 {
		count = 1
		g.Clear = 1
	}
	var cells []gamedata.EventGameReward
	max := uint64(0)
	for _, r := range d.Cells {
		if r.Count > max {
			max = r.Count
		}
	}
	if count > max {
		count = max
	}
	for _, r := range d.Cells {
		if r.Count == count {
			cells = append(cells, r)
		}
	}
	if len(cells) == 0 {
		return fmt.Errorf("eventgames: board rewards missing")
	}
	g.Board = nil
	g.Opened = nil
	g.Lines = nil
	for _, r := range cells {
		g.Board = append(g.Board, r.ID)
	}
	if d.Type == 13 {
		for i := len(g.Board) - 1; i > 0; i-- {
			j, e := s.sample(uint64(i + 1))
			if e != nil {
				return e
			}
			if j >= uint64(i+1) {
				return fmt.Errorf("eventgames: sampler out of range")
			}
			g.Board[i], g.Board[j] = g.Board[j], g.Board[i]
		}
	}
	return nil
}
