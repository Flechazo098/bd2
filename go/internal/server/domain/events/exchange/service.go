package eventexchange

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"

	"time"
)

type Runtime interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
	ConsumeAndGrant(ctx command.Context, _ string, _ []assets.Item, _ []gamedata.BattleReward) ([]byte, error)
}
type progress struct {
	Group, Page, Key, Free uint64
	Reset                  int64
	Counts                 map[string]uint64
}
type receipt struct {
	Fingerprint, Response []byte
	Code                  int
}
type snapshot struct {
	Version  string              `json:"version"`
	Progress map[string]progress `json:"progress"`
	Receipts map[string]receipt  `json:"receipts"`
}
type Service struct {
	store    stateio.Store
	design   *gamedata.EventExchangeCatalog
	registry events.Resolver
	runtime  Runtime
	now      func() time.Time
	draw     func(uint64) (uint64, error)
}

func Open(ctx command.Context, store stateio.Store, design *gamedata.EventExchangeCatalog, registry events.Resolver, runtime Runtime) (*Service, error) {
	if store == nil || design == nil || registry == nil || runtime == nil {
		return nil, fmt.Errorf("eventexchange: invalid configuration")
	}
	s := &Service{store: store, design: design, registry: registry, runtime: runtime, now: time.Now}
	s.draw = func(n uint64) (uint64, error) {
		if n == 0 {
			return 0, fmt.Errorf("eventexchange: empty reward pool")
		}
		v, e := rand.Int(rand.Reader, new(big.Int).SetUint64(n))
		if e != nil {
			return 0, e
		}
		return v.Uint64(), nil
	}
	_, err := s.load(ctx)
	return s, err
}
func (s *Service) load(ctx command.Context) (snapshot, error) {
	st := snapshot{versionconfig.State(), map[string]progress{}, map[string]receipt{}}
	b, err := s.store.Load(ctx.State, "eventexchange")
	if err != nil {
		return st, err
	}
	if b == nil {
		return st, nil
	}
	if err = stateio.RequireExactJSONObject(b, "version", "progress", "receipts"); err != nil {
		return st, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&st); err != nil {
		return st, err
	}
	if st.Version != versionconfig.State() || st.Progress == nil || st.Receipts == nil {
		return st, fmt.Errorf("eventexchange: incompatible state")
	}
	for uid, p := range st.Progress {
		n, e := strconv.ParseUint(uid, 10, 64)
		g, ok := s.design.Groups[p.Group]
		if e != nil || n == 0 || !ok || p.Page < g.StartPage || (!g.Repeat && p.Page > g.EndPage) || p.Counts == nil || p.Free > g.FreeCount {
			return st, fmt.Errorf("eventexchange: invalid saved progress")
		}
		known := map[string]uint64{}
		for _, entry := range g.Page(p.Page) {
			known[strconv.FormatUint(entry.ID, 10)] = entry.SetCount
		}
		for id, count := range p.Counts {
			limit, ok := known[id]
			if !ok || count > limit {
				return st, fmt.Errorf("eventexchange: invalid reward count")
			}
		}
	}
	return st, nil
}
func (s *Service) save(ctx command.Context, st snapshot) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.store.Save(ctx.State, "eventexchange", b)
}
func (s *Service) active(uid uint64) (events.Schedule, gamedata.EventExchangeGroup, error) {
	a, e := s.registry.Resolve(uid)
	if e != nil {
		return a, gamedata.EventExchangeGroup{}, e
	}
	g, ok := s.design.Groups[a.ID]
	now := s.now().UnixMilli()
	if !ok || a.Type != 7 || now < a.Start || now >= a.End {
		return a, g, fmt.Errorf("eventexchange: event unavailable")
	}
	return a, g, nil
}
func (s *Service) init(st *snapshot, a events.Schedule, g gamedata.EventExchangeGroup) progress {
	k := strconv.FormatUint(a.UID, 10)
	p, ok := st.Progress[k]
	if !ok {
		p = progress{Group: g.ID, Page: g.StartPage, Free: g.FreeCount, Counts: map[string]uint64{}}
	}
	if g.FreeType == 2 {
		now := s.now()
		if p.Reset <= now.UnixMilli() {
			p.Free = g.FreeCount
			p.Reset = now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).UnixMilli()
		}
	}
	return p
}

func exhausted(g gamedata.EventExchangeGroup, p progress) bool {
	for _, e := range g.Page(p.Page) {
		if p.Counts[strconv.FormatUint(e.ID, 10)] < e.SetCount {
			return false
		}
	}
	return true
}
func canAdvance(g gamedata.EventExchangeGroup, p progress) bool {
	if p.Page >= g.EndPage && !g.Repeat {
		return false
	}
	var keys uint64
	for _, e := range g.Page(p.Page) {
		if e.KeyType == 1 {
			keys++
		}
	}
	return exhausted(g, p) || (keys > 0 && p.Key >= keys)
}
func advance(g gamedata.EventExchangeGroup, p *progress) error {
	if p.Page >= math.MaxInt32 {
		return fmt.Errorf("eventexchange: page overflow")
	}
	if p.Page >= g.EndPage && !g.Repeat {
		return fmt.Errorf("eventexchange: final page")
	}
	p.Page++
	p.Key = 0
	p.Counts = map[string]uint64{}
	return nil
}
