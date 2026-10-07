package commerce

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"

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
	store        stateio.Store
	design       *gamedata.LoginPassCatalog
	economy      Economy
	items        ClearPackageInventory
	available    func(ctx command.Context, _ uint64) bool
	now          func() time.Time
	resetSeconds int64
}

func NewLoginPasses(ctx command.Context, store stateio.Store, design *gamedata.LoginPassCatalog, economy Economy, items ClearPackageInventory, available func(ctx command.Context, _ uint64) bool) (*LoginPasses, error) {
	if store == nil || design == nil || economy == nil || items == nil || available == nil {
		return nil, fmt.Errorf("commerce: invalid login-pass dependencies")
	}
	s := &LoginPasses{store: store, design: design, economy: economy, items: items, available: available, now: time.Now}
	_, err := s.load(ctx)
	return s, err
}
func (s *LoginPasses) SetClock(now func() time.Time, resetSeconds int64) {
	s.now = now
	s.resetSeconds = resetSeconds
}
func (s *LoginPasses) load(ctx command.Context) (loginPassState, error) {
	v := loginPassState{Progress: map[uint64]loginPassProgress{}, Receipts: map[string]loginPassReceipt{}}
	raw, err := s.store.Load(ctx.State, "commerce_login_passes")
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
func (s *LoginPasses) paid(ctx command.Context, ticket uint64) bool {
	for _, i := range s.items.All(ctx) {
		if i.Type == 19 && i.ID == ticket && i.Count > 0 && (i.ExpiryTime == 0 || i.ExpiryTime > uint64(s.now().UnixMilli())) {
			return true
		}
	}
	return false
}
