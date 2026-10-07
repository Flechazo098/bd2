package commerce

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"

	"time"
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
	store        stateio.Store
	base         Economy
	graph        grantedResolver
	items        *assets.Inventory
	design       *gamedata.CashEntitlementDesign
	now          func() time.Time
	resetSeconds int64
	mail         CashMailIssuer
}

func NewEntitlementEconomy(ctx command.Context, store stateio.Store, base Economy, graph grantedResolver, items *assets.Inventory, design *gamedata.CashEntitlementDesign) (*EntitlementEconomy, error) {
	if store == nil || base == nil || graph == nil || items == nil || design == nil {
		return nil, fmt.Errorf("commerce: missing entitlement dependency")
	}
	e := &EntitlementEconomy{store: store, base: base, graph: graph, items: items, design: design, now: time.Now}
	_, err := e.load(ctx)
	return e, err
}
func (e *EntitlementEconomy) SetClock(now func() time.Time, resetSeconds int64) {
	e.now = now
	e.resetSeconds = resetSeconds
}
func (e *EntitlementEconomy) day() string {
	return e.now().UTC().Add(-time.Duration(e.resetSeconds) * time.Second).Format("2006-01-02")
}
func (e *EntitlementEconomy) load(ctx command.Context) (entitlementState, error) {
	s := entitlementState{Receipts: map[string]entitlementReceipt{}, Subscriptions: map[string]cashSubscription{}}
	raw, err := e.store.Load(ctx.State, "commerce_entitlements")
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
func (e *EntitlementEconomy) save(ctx command.Context, s entitlementState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return e.store.Save(ctx.State, "commerce_entitlements", b)
}
