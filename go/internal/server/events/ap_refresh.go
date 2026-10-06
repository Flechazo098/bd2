package events

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"time"
)

type economyAPClock struct {
	Next int64 `json:"next"`
}

func (e *Economy) AttachAPRefresh(caps map[uint64]uint64, design gamedata.HuntingAPDesign) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for typ, max := range caps {
		if (typ != 30 && typ != 32) || max == 0 || max > math.MaxInt32 {
			return fmt.Errorf("events: invalid free AP refresh")
		}
	}
	e.apCaps = caps
	e.resetSeconds = design.ResetSeconds
	e.now = time.Now
	return nil
}
func (e *Economy) refreshAP(s *economySnapshot) error {
	if len(e.apCaps) == 0 {
		return nil
	}
	now := e.now().UnixMilli()
	offset := (e.resetSeconds - 9*3600) * 1000
	next := ((now-offset)/86400000+1)*86400000 + offset
	b, err := e.store.Load("eventapclock")
	if err != nil {
		return err
	}
	clock := economyAPClock{Next: next}
	reset := b == nil
	if b != nil {
		if err = stateio.RequireExactJSONObject(b, "next"); err != nil {
			return err
		}
		if err = json.Unmarshal(b, &clock); err != nil {
			return err
		}
		reset = now >= clock.Next
	}
	if !reset {
		return nil
	}
	maps.Copy(s.Balances, e.apCaps)
	clock.Next = next
	raw, err := json.Marshal(clock)
	if err != nil {
		return err
	}
	if err = e.store.Save("eventapclock", raw); err != nil {
		return err
	}
	raw, err = json.Marshal(s)
	if err != nil {
		return err
	}
	return e.store.Save("event_economy", raw)
}
func (e *Economy) CanApply(costs []gamedata.Reward) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.load()
	if err != nil {
		return err
	}
	if err = e.refreshAP(&s); err != nil {
		return err
	}
	var wallet, ap []gamedata.Reward
	var items []player.Item
	totals := map[[2]uint64]uint64{}
	for _, r := range costs {
		k := [2]uint64{r.Type, r.ID}
		if r.Count == 0 || r.Count > math.MaxInt32 || totals[k] > math.MaxInt32-r.Count {
			return fmt.Errorf("events: invalid cost")
		}
		totals[k] += r.Count
	}
	for k, n := range totals {
		r := gamedata.Reward{Type: k[0], ID: k[1], Count: n}
		switch {
		case walletCurrency(r.Type):
			wallet = append(wallet, r)
		case apCurrency(r.Type):
			ap = append(ap, r)
		case extraCurrency(r.Type):
			if r.ID != 0 || s.Balances[r.Type] < n {
				return fmt.Errorf("events: insufficient currency")
			}
		case inventoryType(r.Type):
			selected, err := e.items.SelectMutable(r.Type, r.ID, n)
			if err != nil {
				return err
			}
			items = append(items, selected...)
		default:
			return fmt.Errorf("events: unsupported cost type %d", r.Type)
		}
	}
	if err = e.wallet.CanExchange(wallet, nil); err != nil {
		return err
	}
	if len(ap) > 0 {
		if e.hunting == nil {
			return fmt.Errorf("events: AP unavailable")
		}
		if err = e.hunting.CanExchangeAP(ap, nil); err != nil {
			return err
		}
	}
	if len(items) > 0 {
		return e.items.CanConsume(items)
	}
	return nil
}

func (e *Economy) ChargeInfo() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.load()
	if err != nil {
		return nil, err
	}
	if err = e.refreshAP(&s); err != nil {
		return nil, err
	}
	raw, err := e.store.Load("eventapclock")
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var clock economyAPClock
	if err = json.Unmarshal(raw, &clock); err != nil {
		return nil, err
	}
	var out []byte
	for _, typ := range []uint64{30, 32} {
		if _, ok := e.apCaps[typ]; !ok {
			continue
		}
		info := wire.AppendVarint(nil, 1, uint64(clock.Next-86400000))
		item := wire.AppendVarint(nil, 3, typ)
		item = wire.AppendVarint(item, 4, s.Balances[typ])
		info = wire.AppendBytes(info, 2, item)
		out = wire.AppendBytes(out, 1, info)
	}
	return out, nil
}
