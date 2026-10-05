package commerce

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type grantedResolver interface {
	ResolveGranted([]gamedata.BattleReward) ([]gamedata.BattleReward, error)
}
type entitlementReceipt struct {
	Definition []byte `json:"definition"`
	Bundle     []byte `json:"bundle"`
}
type cashSubscription struct {
	Start, Expiry int64
	Claimed       uint64
	LastDay       string
}
type entitlementState struct {
	Receipts      map[string]entitlementReceipt `json:"receipts"`
	Subscriptions map[string]cashSubscription   `json:"subscriptions"`
}
type EntitlementEconomy struct {
	mu           sync.Mutex
	store        stateio.Store
	base         Economy
	graph        grantedResolver
	items        *player.Inventory
	design       *gamedata.CashEntitlementDesign
	now          func() time.Time
	resetSeconds int64
}

func NewEntitlementEconomy(store stateio.Store, base Economy, graph grantedResolver, items *player.Inventory, design *gamedata.CashEntitlementDesign) (*EntitlementEconomy, error) {
	if store == nil || base == nil || graph == nil || items == nil || design == nil {
		return nil, fmt.Errorf("commerce: missing entitlement dependency")
	}
	e := &EntitlementEconomy{store: store, base: base, graph: graph, items: items, design: design, now: time.Now}
	_, err := e.load()
	return e, err
}
func (e *EntitlementEconomy) SetClock(now func() time.Time, resetSeconds int64) {
	e.now = now
	e.resetSeconds = resetSeconds
}
func (e *EntitlementEconomy) day() string {
	return e.now().UTC().Add(-time.Duration(e.resetSeconds) * time.Second).Format("2006-01-02")
}
func (e *EntitlementEconomy) load() (entitlementState, error) {
	s := entitlementState{Receipts: map[string]entitlementReceipt{}, Subscriptions: map[string]cashSubscription{}}
	raw, err := e.store.Load("commerce_entitlements")
	if err != nil || raw == nil {
		return s, err
	}
	if err = stateio.RequireExactJSONObject(raw, "receipts", "subscriptions"); err != nil {
		return s, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	if s.Receipts == nil || s.Subscriptions == nil {
		return s, fmt.Errorf("commerce: malformed entitlement state")
	}
	for id, r := range s.Receipts {
		if id == "" || len(r.Definition) != sha256.Size {
			return s, fmt.Errorf("commerce: malformed entitlement receipt")
		}
	}
	for id, sub := range s.Subscriptions {
		ticket, err := strconv.ParseUint(id, 10, 64)
		if err != nil || len(e.design.Attendance[ticket]) == 0 || sub.Start <= 0 || sub.Expiry != 0 && sub.Expiry < sub.Start || sub.Claimed == 0 || sub.LastDay == "" {
			return s, fmt.Errorf("commerce: malformed subscription")
		}
	}
	return s, nil
}
func (e *EntitlementEconomy) save(s entitlementState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return e.store.Save("commerce_entitlements", b)
}
func (e *EntitlementEconomy) Apply(identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.apply(identity, costs, rewards)
}
func (e *EntitlementEconomy) apply(identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	if identity == "" {
		return nil, fmt.Errorf("commerce: missing entitlement identity")
	}
	definition, _ := json.Marshal(struct{ Costs, Rewards []gamedata.Reward }{costs, rewards})
	digest := sha256.Sum256(definition)
	s, err := e.load()
	if err != nil {
		return nil, err
	}
	if r, ok := s.Receipts[identity]; ok {
		if !bytes.Equal(r.Definition, digest[:]) {
			return nil, fmt.Errorf("commerce: entitlement identity reused")
		}
		return append([]byte(nil), r.Bundle...), nil
	}
	input := make([]gamedata.BattleReward, len(rewards))
	for i, r := range rewards {
		input[i] = gamedata.BattleReward{Type: r.Type, ID: r.ID, Count: r.Count}
	}
	leaves, err := e.graph.ResolveGranted(input)
	if err != nil {
		return nil, err
	}
	// Some product boxes include the first attendance reward (draw-ticket
	// subscriptions); others include only the upfront paid currency. Add only
	// the missing first-day components on activation, then mark row one claimed.
	initial := append([]gamedata.BattleReward(nil), leaves...)
	for _, r := range initial {
		rows := e.design.Attendance[r.ID]
		if r.Type != 19 || len(rows) == 0 {
			continue
		}
		sub := s.Subscriptions[strconv.FormatUint(r.ID, 10)]
		if sub.Start != 0 && (sub.Expiry == 0 || sub.Expiry > e.now().UnixMilli()) {
			continue
		}
		first, resolveErr := e.graph.ResolveGranted([]gamedata.BattleReward{rows[0].Reward})
		if resolveErr != nil {
			return nil, resolveErr
		}
		available := map[[2]uint64]uint64{}
		for _, leaf := range leaves {
			k := [2]uint64{leaf.Type, leaf.ID}
			available[k] += leaf.Count
		}
		for _, leaf := range first {
			k := [2]uint64{leaf.Type, leaf.ID}
			if available[k] < leaf.Count {
				leaf.Count -= available[k]
				leaves = append(leaves, leaf)
			}
		}
	}
	var regular []gamedata.Reward
	var special []player.Item
	for _, r := range leaves {
		if r.Count == 0 || r.Count > math.MaxInt32 {
			return nil, fmt.Errorf("commerce: invalid entitlement quantity")
		}
		switch {
		case r.Type == 62:
			if !e.design.AvatarSets[r.ID] {
				return nil, fmt.Errorf("commerce: unknown avatar set %d", r.ID)
			}
			// The shared gameplay economy expands AvatarSetTable members and
			// emits real AvatarItem/AvatarMotion/AvatarChar ownership.
			regular = append(regular, gamedata.Reward(r))
		case r.Type == 19 && e.design.TicketTypes[r.ID] == 2:
			if len(e.design.Attendance[r.ID]) == 0 {
				return nil, fmt.Errorf("commerce: subscription reward schedule missing")
			}
			now := e.now().UnixMilli()
			expiry := int64(e.items.ContentTicketExpiry(r.ID))
			if expiry < now {
				expiry = now
			}
			if r.Count > uint64((math.MaxInt64-expiry)/(30*86400000)) {
				return nil, fmt.Errorf("commerce: subscription expiry overflow")
			}
			expiry += int64(r.Count) * 30 * 86400000
			special = append(special, player.Item{Type: 19, ID: r.ID, Count: r.Count, ExpiryTime: uint64(expiry), TimeValue: uint64(e.now().UnixMilli())})
			key := strconv.FormatUint(r.ID, 10)
			sub := s.Subscriptions[key]
			if sub.Start == 0 || sub.Expiry <= now {
				sub = cashSubscription{Start: now, Claimed: 1, LastDay: e.day()}
			}
			sub.Expiry = expiry
			s.Subscriptions[key] = sub
		default:
			regular = append(regular, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count})
			if r.Type == 19 && e.design.TicketTypes[r.ID] == 3 && len(e.design.Attendance[r.ID]) > 0 {
				key := strconv.FormatUint(r.ID, 10)
				if _, exists := s.Subscriptions[key]; !exists {
					s.Subscriptions[key] = cashSubscription{Start: e.now().UnixMilli(), Claimed: 1, LastDay: e.day()}
				}
			}
		}
	}
	bundle, err := e.base.Apply(identity+":base", costs, regular)
	if err != nil {
		return nil, err
	}
	items, err := e.items.GrantCommerceOnce(identity+":special", special)
	if err != nil {
		return nil, err
	}
	for _, i := range items {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(i))
		bundle = wire.AppendBytes(bundle, 6, player.ItemWire(player.Item{ID: i.ID, Type: i.Type, Count: i.Count}))
	}
	s.Receipts[identity] = entitlementReceipt{Definition: append([]byte(nil), digest[:]...), Bundle: append([]byte(nil), bundle...)}
	if err = e.save(s); err != nil {
		return nil, err
	}
	return bundle, nil
}

// ClaimSubscriptions grants the next row once per server reset day. Missing
// login days are not retroactively claimed. First-row purchase rewards are
// already present in the product box, matching the client's first-row marker.
func (e *EntitlementEconomy) ClaimSubscriptions(identity string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if identity == "" {
		return nil, fmt.Errorf("commerce: missing attendance receipt identity")
	}
	s, err := e.load()
	if err != nil {
		return nil, err
	}
	receiptKey := "attendance-reply:" + identity
	if receipt, ok := s.Receipts[receiptKey]; ok {
		return append([]byte(nil), receipt.Bundle...), nil
	}
	keys := make([]string, 0, len(s.Subscriptions))
	for k := range s.Subscriptions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var bundle []byte
	for _, key := range keys {
		sub := s.Subscriptions[key]
		ticket, _ := strconv.ParseUint(key, 10, 64)
		rows := e.design.Attendance[ticket]
		if sub.LastDay == e.day() || sub.Expiry != 0 && sub.Expiry <= e.now().UnixMilli() || sub.Expiry == 0 && sub.Claimed >= uint64(len(rows)) {
			continue
		}
		index := sub.Claimed % uint64(len(rows))
		r := rows[index]
		grant, err := e.apply(fmt.Sprintf("commerce:attendance:%s:%s", key, e.day()), nil, []gamedata.Reward{{Type: r.Reward.Type, ID: r.Reward.ID, Count: r.Reward.Count}})
		if err != nil {
			return nil, err
		}
		bundle = append(bundle, grant...)
		// apply persists its receipt; reload before saving progress so it survives.
		latest, err := e.load()
		if err != nil {
			return nil, err
		}
		sub.Claimed++
		sub.LastDay = e.day()
		latest.Subscriptions[key] = sub
		if err = e.save(latest); err != nil {
			return nil, err
		}
		s = latest
	}
	latest, err := e.load()
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(receiptKey))
	latest.Receipts[receiptKey] = entitlementReceipt{Definition: append([]byte(nil), digest[:]...), Bundle: append([]byte(nil), bundle...)}
	if err = e.save(latest); err != nil {
		return nil, err
	}
	return bundle, nil
}
func (e *EntitlementEconomy) MergeAttendance(response []byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.load()
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), response...)
	keys := make([]string, 0, len(s.Subscriptions))
	for k := range s.Subscriptions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sub := s.Subscriptions[key]
		ticket, _ := strconv.ParseUint(key, 10, 64)
		if sub.Expiry != 0 {
			b := wire.AppendVarint(nil, 1, ticket)
			b = wire.AppendVarint(b, 2, uint64(sub.Start))
			b = wire.AppendVarint(b, 3, uint64(sub.Expiry))
			out = wire.AppendBytes(out, 3, b)
		}
		if typ := e.design.AttendanceTypes[ticket]; typ != 0 {
			rewards := []byte{}
			n := sub.Claimed
			if n > uint64(len(e.design.Attendance[ticket])) {
				n = uint64(len(e.design.Attendance[ticket]))
			}
			for i := uint64(1); i <= n; i++ {
				rewards = wire.AppendVarint(rewards, 1, i)
			}
			entry := wire.AppendVarint(nil, 1, typ)
			entry = wire.AppendBytes(entry, 2, rewards)
			out = wire.AppendBytes(out, 7, entry)
		} else {
			for i := uint64(1); i <= sub.Claimed; i++ {
				b := wire.AppendVarint(nil, 1, ticket)
				b = wire.AppendVarint(b, 2, i)
				b = wire.AppendVarint(b, 3, 1)
				out = wire.AppendBytes(out, 4, b)
			}
		}
	}
	return out, nil
}
