// Package eventgames implements this server's persistent seasonal minigames.
package eventgames

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Resolver interface {
	Resolve(uint64) (events.Schedule, error)
}
type Rewards interface {
	ConsumeAndGrant(string, []player.Item, []gamedata.BattleReward) ([]byte, error)
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
type Service struct {
	mu       sync.Mutex
	store    stateio.Store
	resolver Resolver
	rewards  Rewards
	state    snapshot
	load     func(uint64, uint64) (*gamedata.EventGame, error)
	sample   func(uint64) (uint64, error)
	now      func() time.Time
}

func Open(store stateio.Store, root, version string, resolver Resolver, rewards Rewards) (*Service, error) {
	if store == nil || resolver == nil || rewards == nil {
		return nil, fmt.Errorf("eventgames: incomplete runtime")
	}
	s := &Service{store: store, resolver: resolver, rewards: rewards, now: time.Now, state: snapshot{1, map[string]GameState{}, map[string]reply{}}}
	cache := map[string]*gamedata.EventGame{}
	s.load = func(kind, id uint64) (*gamedata.EventGame, error) {
		k := fmt.Sprintf("%d:%d", kind, id)
		if d := cache[k]; d != nil {
			return d, nil
		}
		d, e := gamedata.LoadEventGame(root, version, kind, id)
		if e == nil {
			cache[k] = d
		}
		return d, e
	}
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
	raw, e := store.Load("eventgames")
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
func contains(a []uint64, v uint64) bool {
	return slices.Contains(a, v)
}
func (s *Service) Handle(path string, req []byte) (int, []byte, bool, error) {
	return s.HandleSession(path, req, "local")
}

var routes = map[string]struct {
	Code int
	Kind uint64
}{"/MiniGameBoardInfo": {223, 12}, "/MiniGameBoardPlay": {224, 12}, "/MiniGameBingoInfo": {256, 13}, "/MiniGameBingoPlay": {257, 13}, "/MiniPuzzleInfo": {343, 17}, "/MiniPuzzleOpen": {344, 17}, "/MiniPuzzleAllOpen": {345, 17}, "/MiniPuzzleRenew": {346, 17}, "/MiniGameRouletteInfo": {415, 19}, "/MiniGameRouletteDraw": {416, 19}}

func (s *Service) HandleSession(path string, req []byte, session string) (int, []byte, bool, error) {
	r, ok := routes[path]
	if !ok {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	key := fmt.Sprintf("%s:%s:%d", session, path, seq)
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
			consumed := []player.Item(nil)
			if len(items) > 0 {
				consumed = items
				items = nil
			}
			b, e := s.rewards.ConsumeAndGrant(fmt.Sprintf("eventgames:%s:%d", key, field), consumed, rr)
			if e != nil {
				return fail(e)
			}
			bundles[field] = b
		}
		if len(items) > 0 {
			if _, e = s.rewards.ConsumeAndGrant("eventgames:"+key+":cost", items, nil); e != nil {
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
	if e = s.store.Save("eventgames", payload); e != nil {
		return fail(e)
	}
	s.state = next
	return r.Code, out, true, nil
}
func consume(req []byte, n int, d *gamedata.EventGame, count uint64) ([]player.Item, error) {
	var out []player.Item
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
		out = append(out, player.Item{InvenIndex: index, ID: id, Type: typ, Count: qty})
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
