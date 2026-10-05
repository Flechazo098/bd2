package eventexchange

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Runtime interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
	ConsumeAndGrant(string, []player.Item, []gamedata.BattleReward) ([]byte, error)
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
	mu       sync.Mutex
	store    stateio.Store
	design   *gamedata.EventExchangeCatalog
	registry events.Resolver
	runtime  Runtime
	now      func() time.Time
	draw     func(uint64) (uint64, error)
}

func Open(store stateio.Store, design *gamedata.EventExchangeCatalog, registry events.Resolver, runtime Runtime) (*Service, error) {
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
	_, err := s.load()
	return s, err
}
func (s *Service) load() (snapshot, error) {
	st := snapshot{versionconfig.State(), map[string]progress{}, map[string]receipt{}}
	b, err := s.store.Load("eventexchange")
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
func (s *Service) save(st snapshot) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.store.Save("eventexchange", b)
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
func (s *Service) Handle(path string, req []byte) (int, []byte, bool, error) {
	return s.HandleSession(path, req, "local")
}
func (s *Service) HandleSession(path string, req []byte, session string) (int, []byte, bool, error) {
	code, ok := map[string]int{"/EventExchangeInfo": 183, "/EventExchangeReward": 184, "/EventExchangeNextPageOpen": 185}[path]
	if !ok {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, fmt.Errorf("eventexchange: invalid sequence")
	}
	st, err := s.load()
	if err != nil {
		return code, nil, true, err
	}
	key := session + ":" + strconv.FormatUint(seq, 10)
	fingerprint := append([]byte(path), req...)
	if r, ok := st.Receipts[key]; ok {
		if !bytes.Equal(fingerprint, r.Fingerprint) {
			return code, nil, true, fmt.Errorf("eventexchange: sequence conflict")
		}
		return r.Code, r.Response, true, nil
	}
	var out []byte
	if path == "/EventExchangeInfo" {
		rows := s.registry.List()
		sort.Slice(rows, func(i, j int) bool { return rows[i].UID < rows[j].UID })
		for _, a := range rows {
			_, g, e := s.active(a.UID)
			if e != nil {
				continue
			}
			p := s.init(&st, a, g)
			st.Progress[strconv.FormatUint(a.UID, 10)] = p
			out = wire.AppendBytes(out, 1, progressWire(a.UID, p))
			for _, entry := range g.Page(p.Page) {
				out = wire.AppendBytes(out, 2, countWire(a.UID, g.ID, entry.ID, p.Counts[strconv.FormatUint(entry.ID, 10)]))
			}
		}
		if err = s.save(st); err != nil {
			return code, nil, true, err
		}
		return code, out, true, nil
	}
	uid, _, e := wire.Varint(req, 2)
	if e != nil || uid == 0 {
		return code, nil, true, fmt.Errorf("eventexchange: invalid event UID")
	}
	a, g, err := s.active(uid)
	if err != nil {
		return code, nil, true, err
	}
	p := s.init(&st, a, g)
	if path == "/EventExchangeNextPageOpen" {
		group, _, e := wire.Varint(req, 3)
		if e != nil || group != g.ID {
			return code, nil, true, fmt.Errorf("eventexchange: group mismatch")
		}
		if !canAdvance(g, p) {
			return code, nil, true, fmt.Errorf("eventexchange: next page locked")
		}
		if err = advance(g, &p); err != nil {
			return code, nil, true, err
		}
	} else {
		count, _, e := wire.Varint(req, 3)
		if e != nil || count == 0 || count > 10 {
			return code, nil, true, fmt.Errorf("eventexchange: count must be 1..10")
		}
		uses, e := parseUses(req)
		if e != nil {
			return code, nil, true, e
		}
		free := len(uses) == 0
		if free {
			if g.FreeCount == 0 || p.Free < count {
				return code, nil, true, fmt.Errorf("eventexchange: free draws exhausted")
			}
			p.Free -= count
		} else {
			if g.Cost.Count > math.MaxInt32/count {
				return code, nil, true, fmt.Errorf("eventexchange: cost overflow")
			}
			want := g.Cost.Count * count
			var total uint64
			for _, item := range uses {
				if item.Type != g.Cost.Type || item.ID != g.Cost.ID || item.Count == 0 || item.Count > math.MaxInt32 || total > math.MaxInt32-item.Count {
					return code, nil, true, fmt.Errorf("eventexchange: invalid consume items")
				}
				total += item.Count
			}
			if total != want {
				return code, nil, true, fmt.Errorf("eventexchange: consume count mismatch")
			}
		}
		var rewards []gamedata.Reward
		var changed []byte
		for n := uint64(0); n < count; n++ {
			entries := g.Page(p.Page)
			var weights []uint64
			var sum uint64
			for _, entry := range entries {
				remaining := entry.SetCount - p.Counts[strconv.FormatUint(entry.ID, 10)]
				weight := entry.Ratio
				var draws uint64
				for _, c := range p.Counts {
					draws += c
				}
				if g.UnlockRatio > 0 && draws+1 < g.UnlockRatio {
					weight = entry.LimitedRatio
				}
				if remaining == 0 {
					weight = 0
				}
				if sum > math.MaxUint64-weight {
					return code, nil, true, fmt.Errorf("eventexchange: weight sum overflow")
				}
				sum += weight
				weights = append(weights, weight)
			}
			if sum == 0 {
				return code, nil, true, fmt.Errorf("eventexchange: reward pool exhausted")
			}
			v, e := s.draw(sum)
			if e != nil || v >= sum {
				return code, nil, true, fmt.Errorf("eventexchange: random source failure")
			}
			for i, entry := range entries {
				if v < weights[i] {
					id := strconv.FormatUint(entry.ID, 10)
					p.Counts[id]++
					if entry.KeyType == 1 && p.Counts[id] == entry.SetCount {
						p.Key++
					}
					rewards = append(rewards, entry.Reward)
					changed = wire.AppendBytes(changed, 2, countWire(uid, g.ID, entry.ID, p.Counts[id]))
					break
				}
				v -= weights[i]
			}
			if exhausted(g, p) && (p.Page < g.EndPage || g.Repeat) {
				if e := advance(g, &p); e != nil {
					return code, nil, true, e
				}
			}
		}
		var bundle []byte
		if free {
			bundle, err = s.runtime.Apply("eventexchange:"+key, nil, rewards)
		} else {
			currency := g.Cost.Type == 2 || g.Cost.Type == 3 || g.Cost.Type == 4 || g.Cost.Type == 12 || g.Cost.Type == 20
			if currency {
				cost := g.Cost
				cost.Count *= count
				bundle, err = s.runtime.Apply("eventexchange:"+key, []gamedata.Reward{cost}, rewards)
			} else {
				rs := make([]gamedata.BattleReward, len(rewards))
				for i, r := range rewards {
					rs[i] = gamedata.BattleReward{Type: r.Type, ID: r.ID, Count: r.Count}
				}
				bundle, err = s.runtime.ConsumeAndGrant("eventexchange:"+key, uses, rs)
			}
		}
		if err != nil {
			return code, nil, true, err
		}
		out = wire.AppendBytes(out, 1, bundle)
		out = append(out, changed...)
		out = wire.AppendBytes(out, 3, progressWire(uid, p))
	}
	st.Progress[strconv.FormatUint(uid, 10)] = p
	st.Receipts[key] = receipt{fingerprint, out, code}
	if err = s.save(st); err != nil {
		return code, nil, true, err
	}
	return code, out, true, nil
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
func progressWire(uid uint64, p progress) []byte {
	var out []byte
	for _, f := range []struct {
		n int
		v uint64
	}{{1, uid}, {2, p.Group}, {3, p.Page}, {4, p.Key}, {5, p.Free}, {6, uint64(p.Reset)}} {
		if f.v > 0 {
			out = wire.AppendVarint(out, f.n, f.v)
		}
	}
	return out
}
func countWire(uid, group, id, count uint64) []byte {
	b := wire.AppendVarint(nil, 1, uid)
	b = wire.AppendVarint(b, 2, group)
	b = wire.AppendVarint(b, 3, id)
	if count > 0 {
		b = wire.AppendVarint(b, 4, count)
	}
	return b
}
func parseUses(req []byte) ([]player.Item, error) {
	var out []player.Item
	seen := map[uint64]bool{}
	err := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 4 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("eventexchange: malformed use item")
		}
		var item player.Item
		for n, p := range map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count} {
			v, _, e := wire.Varint(f.Value, n)
			if e != nil {
				return e
			}
			*p = v
		}
		if item.Type == 0 || item.Count == 0 || item.InvenIndex > math.MaxInt64 || seen[item.InvenIndex] {
			return fmt.Errorf("eventexchange: invalid or duplicate consume item")
		}
		seen[item.InvenIndex] = true
		out = append(out, item)
		return nil
	})
	return out, err
}
