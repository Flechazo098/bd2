package commerce

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type loginPassProgress struct {
	Free, Premium uint64
	LastDay       string
}
type loginPassReceipt struct {
	Bundle   []byte
	Rewarded map[uint64]bool
}
type loginPassState struct {
	Progress map[uint64]loginPassProgress
	Receipts map[string]loginPassReceipt
}
type LoginPasses struct {
	mu           sync.Mutex
	store        stateio.Store
	design       *gamedata.LoginPassCatalog
	economy      Economy
	items        ClearPackageInventory
	available    func(uint64) bool
	now          func() time.Time
	resetSeconds int64
}

func NewLoginPasses(store stateio.Store, design *gamedata.LoginPassCatalog, economy Economy, items ClearPackageInventory, available func(uint64) bool) (*LoginPasses, error) {
	if store == nil || design == nil || economy == nil || items == nil || available == nil {
		return nil, fmt.Errorf("commerce: invalid login-pass dependencies")
	}
	s := &LoginPasses{store: store, design: design, economy: economy, items: items, available: available, now: time.Now}
	_, err := s.load()
	return s, err
}
func (s *LoginPasses) SetClock(now func() time.Time, resetSeconds int64) {
	s.now = now
	s.resetSeconds = resetSeconds
}
func (s *LoginPasses) load() (loginPassState, error) {
	v := loginPassState{Progress: map[uint64]loginPassProgress{}, Receipts: map[string]loginPassReceipt{}}
	raw, err := s.store.Load("commerce_login_passes")
	if err != nil || raw == nil {
		return v, err
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	if v.Progress == nil || v.Receipts == nil {
		return v, fmt.Errorf("commerce: invalid login-pass state")
	}
	for group, p := range v.Progress {
		rows, ok := s.design.Groups[group]
		if !ok || p.Free > uint64(len(rows)) || p.Premium > p.Free || p.Free > 0 && p.LastDay == "" {
			return v, fmt.Errorf("commerce: invalid login-pass progress")
		}
	}
	return v, nil
}
func (s *LoginPasses) paid(ticket uint64) bool {
	for _, i := range s.items.All() {
		if i.Type == 19 && i.ID == ticket && i.Count > 0 && (i.ExpiryTime == 0 || i.ExpiryTime > uint64(s.now().UnixMilli())) {
			return true
		}
	}
	return false
}

// ClaimAndInfo is called by AttendanceHandler inside the account transaction.
// Free progression advances once per observed reset day, and buying premium
// catches up the already earned rows without advancing the login day count.
func (s *LoginPasses) ClaimAndInfo(identity string) ([]byte, [][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity == "" {
		return nil, nil, fmt.Errorf("commerce: missing login-pass identity")
	}
	v, err := s.load()
	if err != nil {
		return nil, nil, err
	}
	keys := make([]uint64, 0, len(s.design.Groups))
	for group := range s.design.Groups {
		keys = append(keys, group)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	r, seen := v.Receipts[identity]
	if !seen {
		r = loginPassReceipt{Rewarded: map[uint64]bool{}}
		var rewards []gamedata.Reward
		day := s.now().UTC().Add(-time.Duration(s.resetSeconds) * time.Second).Format("2006-01-02")
		for _, group := range keys {
			if !s.available(group) {
				continue
			}
			rows := s.design.Groups[group]
			if len(rows) == 0 {
				continue
			}
			p := v.Progress[group]
			if p.LastDay != day && p.Free < uint64(len(rows)) {
				rewards = append(rewards, rows[p.Free].Free)
				p.Free++
				p.LastDay = day
				r.Rewarded[group] = true
			}
			if s.paid(rows[0].TicketID) {
				for p.Premium < p.Free {
					rewards = append(rewards, rows[p.Premium].Premium)
					p.Premium++
					r.Rewarded[group] = true
				}
			}
			v.Progress[group] = p
		}
		if len(rewards) > 0 {
			r.Bundle, err = s.economy.Apply("login-pass:"+identity, nil, rewards)
			if err != nil {
				return nil, nil, err
			}
		}
		v.Receipts[identity] = r
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		if err = s.store.Save("commerce_login_passes", raw); err != nil {
			return nil, nil, err
		}
	}
	var infos [][]byte
	for _, group := range keys {
		if !s.available(group) {
			continue
		}
		p := v.Progress[group]
		if p.Free == 0 {
			continue
		}
		b := wire.AppendVarint(nil, 1, group)
		b = wire.AppendVarint(b, 2, p.Free)
		if r.Rewarded[group] {
			b = wire.AppendVarint(b, 3, 1)
		}
		infos = append(infos, b)
	}
	return append([]byte(nil), r.Bundle...), infos, nil
}
