package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"
)

type talentDispatchRow struct {
	ID       uint64            `json:"id"`
	Start    int64             `json:"start"`
	End      int64             `json:"end"`
	Identity string            `json:"identity"`
	Rewards  []gamedata.Reward `json:"rewards"`
	Claimed  bool              `json:"claimed"`
	Bundle   []byte            `json:"bundle"`
}
type talentDispatchState struct {
	Claims map[string]talentDispatchClaim `json:"claims"`
	Rows   map[uint64]talentDispatchRow   `json:"rows"`
	Starts map[string][]byte              `json:"starts"`
}
type talentDispatchClaim struct {
	Digest string `json:"digest"`
	Body   []byte `json:"body"`
}

type TalentDispatchService struct {
	session string
	mu      sync.Mutex
	store   stateio.Store
	design  map[uint64]gamedata.TalentDispatchDesign
	now     func() time.Time
	draw    func(uint64) (uint64, error)
	economy interface {
		Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
	}
}

func OpenTalentDispatch(store stateio.Store, d map[uint64]gamedata.TalentDispatchDesign, economy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}) (*TalentDispatchService, error) {
	if store == nil || len(d) == 0 || economy == nil {
		return nil, fmt.Errorf("dispatch: invalid configuration")
	}
	return &TalentDispatchService{store: store, design: d, economy: economy, now: time.Now, draw: func(n uint64) (uint64, error) {
		if n == 0 {
			return 0, fmt.Errorf("dispatch: empty pool")
		}
		v, e := rand.Int(rand.Reader, new(big.Int).SetUint64(n))
		if e != nil {
			return 0, e
		}
		return v.Uint64(), nil
	}}, nil
}
func (s *TalentDispatchService) load() (talentDispatchState, error) {
	st := talentDispatchState{Rows: map[uint64]talentDispatchRow{}, Starts: map[string][]byte{}, Claims: map[string]talentDispatchClaim{}}
	b, e := s.store.Load("talent_dispatch")
	if e != nil || b == nil {
		return st, e
	}
	if e = stateio.RequireExactJSONObject(b, "rows", "starts", "claims"); e != nil {
		return st, e
	}
	if e = json.Unmarshal(b, &st); e != nil {
		return st, e
	}
	if st.Rows == nil || st.Starts == nil || st.Claims == nil {
		return st, fmt.Errorf("dispatch: invalid state")
	}
	for id, r := range st.Rows {
		if _, ok := s.design[id]; !ok || r.ID != id || r.Start <= 0 || r.End <= r.Start || r.Identity == "" {
			return st, fmt.Errorf("dispatch: invalid row")
		}
	}
	return st, nil
}
func (s *TalentDispatchService) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = id
}
func (s *TalentDispatchService) save(st talentDispatchState) error {
	b, e := json.Marshal(st)
	if e != nil {
		return e
	}
	return s.store.Save("talent_dispatch", b)
}
func (s *TalentDispatchService) rowWire(r talentDispatchRow) []byte {
	b := wire.AppendVarint(nil, 1, r.ID)
	b = wire.AppendVarint(b, 2, uint64(s.now().UnixMilli()))
	return wire.AppendVarint(b, 3, uint64(r.End))
}

// Start returns extra TalentSkillUseResponse fields. Its caller owns talent
// cost/experience/cooldown and the encompassing account transaction.
func (s *TalentDispatchService) Start(identity string, character Character, rule gamedata.TalentUseRule, targets []uint64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity == "" || rule.Class != 18 || character.InvenIndex == 0 || len(targets) == 0 {
		return nil, fmt.Errorf("dispatch: invalid start")
	}
	st, e := s.load()
	if e != nil {
		return nil, e
	}
	if b, ok := st.Starts[identity]; ok {
		return append([]byte(nil), b...), nil
	}
	allowed := map[uint64]bool{}
	for _, v := range rule.Values {
		if v > 0 {
			allowed[uint64(v)] = true
		}
	}
	prefabs := map[string]bool{}
	for id, r := range st.Rows {
		if !r.Claimed {
			prefabs[s.design[id].Prefab] = true
		}
	}
	var rows []talentDispatchRow
	for _, id := range targets {
		d, ok := s.design[id]
		if !ok || !allowed[id] || prefabs[d.Prefab] {
			return nil, fmt.Errorf("dispatch: unavailable dispatch %d", id)
		}
		prefabs[d.Prefab] = true
		rs, e := d.Roll(s.draw)
		if e != nil {
			return nil, e
		}
		r := talentDispatchRow{ID: id, Start: s.now().UnixMilli(), Identity: identity}
		r.End = d.EndTime(time.UnixMilli(r.Start)).UnixMilli()
		for _, reward := range rs {
			r.Rewards = append(r.Rewards, gamedata.Reward(reward))
		}
		rows = append(rows, r)
	}
	var b []byte
	for _, r := range rows {
		st.Rows[r.ID] = r
		b = wire.AppendBytes(b, 8, s.rowWire(r))
	}
	st.Starts[identity] = b
	return b, s.save(st)
}
func (s *TalentDispatchService) Handle(path string, req []byte) (int, []byte, bool, error) {
	if path != "/DispatchInfo" && path != "/DispatchReward" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, ok, e := wire.Varint(req, 1)
	if e != nil || !ok || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, fmt.Errorf("dispatch: missing sequence")
	}
	st, e := s.load()
	if e != nil {
		return 0, nil, true, e
	}
	if path == "/DispatchInfo" {
		var b []byte
		ids := []uint64{}
		for id, r := range st.Rows {
			if !r.Claimed {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			b = wire.AppendBytes(b, 1, s.rowWire(st.Rows[id]))
		}
		return 0, b, true, nil
	}
	if s.session == "" {
		return 108, nil, true, fmt.Errorf("dispatch: claim session unavailable")
	}
	var ids []uint64
	e = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("dispatch: invalid ids")
		}
		b := f.Value
		for len(b) > 0 {
			n, k := binary.Uvarint(b)
			if k <= 0 || n == 0 || n > math.MaxInt32 || len(ids) >= len(s.design) {
				return fmt.Errorf("dispatch: invalid packed ids")
			}
			ids = append(ids, n)
			b = b[k:]
		}
		return nil
	})
	if e != nil || len(ids) == 0 {
		return 0, nil, true, fmt.Errorf("dispatch: missing reward ids")
	}
	digest := sha256.Sum256(req)
	key := fmt.Sprintf("%s:%d", s.session, seq)
	if receipt, ok := st.Claims[key]; ok {
		if receipt.Digest != fmt.Sprintf("%x", digest) {
			return 0, nil, true, fmt.Errorf("dispatch: conflicting claim sequence")
		}
		return 108, append([]byte(nil), receipt.Body...), true, nil
	}
	seen := map[uint64]bool{}
	for _, id := range ids {
		r, ok := st.Rows[id]
		if !ok || seen[id] || s.now().UnixMilli() < r.End {
			return 0, nil, true, fmt.Errorf("dispatch: reward not ready")
		}
		seen[id] = true
	}
	var body []byte
	for _, id := range ids {
		r := st.Rows[id]
		if r.Claimed {
			continue
		}
		if !r.Claimed {
			bundle, e := s.economy.Apply(r.Identity+fmt.Sprintf(":dispatch:%d", id), nil, r.Rewards)
			if e != nil {
				return 0, nil, true, e
			}
			r.Bundle = bundle
			r.Claimed = true
			st.Rows[id] = r
		}
		if e = wire.Walk(r.Bundle, func(f wire.Field) error {
			if f.Number == 1 && f.Type == 2 {
				body = wire.AppendBytes(body, 1, f.Value)
			}
			return nil
		}); e != nil {
			return 0, nil, true, e
		}
	}
	st.Claims[key] = talentDispatchClaim{Digest: fmt.Sprintf("%x", digest), Body: body}
	return 108, body, true, s.save(st)
}
