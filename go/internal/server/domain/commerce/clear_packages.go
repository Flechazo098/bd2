package commerce

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"

	"time"
)

type ClearPackageInventory interface {
	All(ctx command.Context) []assets.Item
}
type clearClaim struct{ Kind, GroupID, TicketID, TargetID, Level uint64 }
type clearClaimReceipt struct {
	Claim    clearClaim `json:"claim"`
	Response []byte     `json:"response"`
}
type ClearPackages struct {
	store        stateio.Store
	economy      Economy
	items        ClearPackageInventory
	design       map[clearClaim]gamedata.ClearPackageRewardDesign
	packCleared  func(uint64, uint64) bool
	towerCleared func(uint64, uint64) bool
	now          func() time.Time
}

func NewClearPackages(ctx command.Context, store stateio.Store, design *gamedata.ClearPackageCatalog, economy Economy, items ClearPackageInventory) (*ClearPackages, error) {
	if store == nil || design == nil || economy == nil || items == nil {
		return nil, fmt.Errorf("commerce: invalid clear package dependencies")
	}
	s := &ClearPackages{store: store, economy: economy, items: items, design: map[clearClaim]gamedata.ClearPackageRewardDesign{}, now: time.Now}
	for _, r := range design.Rewards {
		k := clearClaim{r.Kind, r.GroupID, r.TicketID, r.TargetID, r.Level}
		if _, ok := s.design[k]; ok {
			return nil, fmt.Errorf("commerce: duplicate clear reward")
		}
		s.design[k] = r
	}
	_, err := s.load(ctx)
	return s, err
}
func (s *ClearPackages) AttachProgress(pack, tower func(uint64, uint64) bool) {
	s.packCleared = pack
	s.towerCleared = tower
}
func (s *ClearPackages) load(ctx command.Context) (map[string]clearClaimReceipt, error) {
	v := map[string]clearClaimReceipt{}
	raw, err := s.store.Load(ctx.State, "commerce_clear_claims")
	if err != nil || raw == nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v)
	if err == nil && v == nil {
		err = fmt.Errorf("commerce: invalid clear claim state")
	}
	if err == nil {
		for identity, receipt := range v {
			if identity != clearClaimID(receipt.Claim) || len(receipt.Response) == 0 {
				err = fmt.Errorf("commerce: invalid saved clear claim")
				break
			}
			if _, ok := s.design[receipt.Claim]; !ok {
				err = fmt.Errorf("commerce: unknown saved clear claim")
				break
			}
		}
	}
	return v, err
}
func clearClaimID(c clearClaim) string {
	return fmt.Sprintf("clear-package:%d:%d:%d:%d:%d", c.Kind, c.GroupID, c.TicketID, c.TargetID, c.Level)
}
func (s *ClearPackages) entitled(ctx command.Context, ticket uint64) bool {
	for _, item := range s.items.All(ctx) {
		if item.Type == 19 && item.ID == ticket && item.Count > 0 && (item.ExpiryTime == 0 || item.ExpiryTime > uint64(s.now().UnixMilli())) {
			return true
		}
	}
	return false
}
