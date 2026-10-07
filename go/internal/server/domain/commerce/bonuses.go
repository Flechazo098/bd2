package commerce

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type bonusGroup struct{ ProductGroup, ContentsGroup uint64 }
type bonusPurchases interface {
	LifetimePurchaseTotal(ctx command.Context, _ []gamedata.CashProductKey) (uint64, error)
}
type bonusState struct {
	Claims   map[string]bool            `json:"claims"`
	Receipts map[string]purchaseReceipt `json:"receipts"`
}
type CashBonuses struct {
	store    stateio.Store
	economy  Economy
	counts   bonusPurchases
	groups   map[bonusGroup][]gamedata.CashProductKey
	rewards  map[bonusGroup][]gamedata.CashBonusReward
	ordered  []bonusGroup
	claimIDs map[string]bool
}

func bonusClaimID(group bonusGroup, id uint64) string {
	return fmt.Sprintf("%d:%d:%d", group.ProductGroup, group.ContentsGroup, id)
}

func NewCashBonuses(ctx command.Context, store stateio.Store, economy Economy, counts bonusPurchases, design *gamedata.CashBonusCatalog, packages []gamedata.CashPackageDesign) (*CashBonuses, error) {
	if store == nil || economy == nil || counts == nil || design == nil {
		return nil, fmt.Errorf("commerce: missing cash bonus dependency")
	}
	s := &CashBonuses{store: store, economy: economy, counts: counts, groups: map[bonusGroup][]gamedata.CashProductKey{}, rewards: map[bonusGroup][]gamedata.CashBonusReward{}, claimIDs: map[string]bool{}}
	seen := map[gamedata.CashProductKey]bool{}
	for _, p := range packages {
		// EPackages.BonusBundleGroup=8; unrelated packages can reuse a contents ID.
		if p.PackageType != 8 || len(design.Groups[p.ContentsGroupID]) == 0 {
			continue
		}
		k := gamedata.CashProductKey{GroupID: p.GroupID, ProductID: p.ID, SaleGroup: p.SaleGroup}
		if seen[k] {
			return nil, fmt.Errorf("commerce: duplicate cash bonus product %+v", k)
		}
		seen[k] = true
		group := bonusGroup{p.GroupID, p.ContentsGroupID}
		s.groups[group] = append(s.groups[group], k)
		s.rewards[group] = design.Groups[p.ContentsGroupID]
	}
	for group, rows := range s.rewards {
		s.ordered = append(s.ordered, group)
		for _, row := range rows {
			s.claimIDs[bonusClaimID(group, row.ID)] = true
		}
	}
	sort.Slice(s.ordered, func(i, j int) bool {
		if s.ordered[i].ProductGroup != s.ordered[j].ProductGroup {
			return s.ordered[i].ProductGroup < s.ordered[j].ProductGroup
		}
		return s.ordered[i].ContentsGroup < s.ordered[j].ContentsGroup
	})
	_, err := s.load(ctx)
	return s, err
}

func (s *CashBonuses) load(ctx command.Context) (bonusState, error) {
	v := bonusState{Claims: map[string]bool{}, Receipts: map[string]purchaseReceipt{}}
	raw, err := s.store.Load(ctx.State, "commerce_cash_bonuses")
	if err != nil || raw == nil {
		return v, err
	}
	if err = stateio.RequireExactJSONObject(raw, "claims", "receipts"); err != nil {
		return v, err
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	if v.Claims == nil || v.Receipts == nil {
		return v, fmt.Errorf("commerce: malformed cash bonus state")
	}
	for id, claimed := range v.Claims {
		if !claimed || !s.claimIDs[id] {
			return v, fmt.Errorf("commerce: invalid cash bonus claim %s", id)
		}
	}
	for id, r := range v.Receipts {
		digest, err := hex.DecodeString(r.Digest)
		if id == "" || err != nil || len(digest) != sha256.Size || len(r.Response) == 0 {
			return v, fmt.Errorf("commerce: invalid cash bonus receipt")
		}
	}
	return v, nil
}
