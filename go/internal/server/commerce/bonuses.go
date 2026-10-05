package commerce

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
)

type bonusGroup struct{ ProductGroup, ContentsGroup uint64 }
type bonusPurchases interface {
	LifetimePurchaseTotal([]gamedata.CashProductKey) (uint64, error)
}
type bonusState struct {
	Claims   map[string]bool            `json:"claims"`
	Receipts map[string]purchaseReceipt `json:"receipts"`
}
type CashBonuses struct {
	mu       sync.Mutex
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

func NewCashBonuses(store stateio.Store, economy Economy, counts bonusPurchases, design *gamedata.CashBonusCatalog, packages []gamedata.CashPackageDesign) (*CashBonuses, error) {
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
	_, err := s.load()
	return s, err
}

func (s *CashBonuses) load() (bonusState, error) {
	v := bonusState{Claims: map[string]bool{}, Receipts: map[string]purchaseReceipt{}}
	raw, err := s.store.Load("commerce_cash_bonuses")
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

func (s *CashBonuses) rewarded(v bonusState, group bonusGroup, field int) []byte {
	var out []byte
	for _, row := range s.rewards[group] {
		if v.Claims[bonusClaimID(group, row.ID)] {
			out = wire.AppendVarint(out, field, row.ID)
		}
	}
	return out
}

func (s *CashBonuses) Handle(path string, request []byte) (int, []byte, bool, error) {
	return s.HandleSession(path, request, "")
}

func (s *CashBonuses) HandleSession(path string, request []byte, session string) (int, []byte, bool, error) {
	code := 588
	if path == "/CashBonusReward" {
		code = 589
	} else if path != "/CashBonusInfo" {
		return 0, nil, false, nil
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, fmt.Errorf("commerce: invalid cash bonus sequence")
	}
	if err = wire.Walk(request, func(wire.Field) error { return nil }); err != nil {
		return code, nil, true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.load()
	if err != nil {
		return code, nil, true, err
	}
	if path == "/CashBonusInfo" {
		var out []byte
		for _, group := range s.ordered {
			n, err := s.counts.LifetimePurchaseTotal(s.groups[group])
			if err != nil {
				return code, nil, true, err
			}
			// Preserve the native empty response for groups with no purchases.
			if n == 0 {
				continue
			}
			b := wire.AppendVarint(nil, 1, group.ProductGroup)
			b = wire.AppendVarint(b, 2, group.ContentsGroup)
			b = wire.AppendVarint(b, 3, n)
			b = append(b, s.rewarded(v, group, 4)...)
			out = wire.AppendBytes(out, 1, b)
		}
		return code, out, true, nil
	}
	if session == "" {
		return code, nil, true, fmt.Errorf("commerce: cash bonus session unavailable")
	}
	identity := fmt.Sprintf("cash-bonus:%x:%d", sha256.Sum256([]byte(session)), seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	if receipt, ok := v.Receipts[identity]; ok {
		if receipt.Digest != digest {
			return code, nil, true, fmt.Errorf("commerce: conflicting cash bonus replay")
		}
		return code, append([]byte(nil), receipt.Response...), true, nil
	}
	groupID, _, err := wire.Varint(request, 2)
	if err != nil {
		return code, nil, true, err
	}
	contents, _, err := wire.Varint(request, 3)
	if err != nil {
		return code, nil, true, err
	}
	id, _, err := wire.Varint(request, 4)
	if err != nil {
		return code, nil, true, err
	}
	group := bonusGroup{groupID, contents}
	var chosen *gamedata.CashBonusReward
	for _, row := range s.rewards[group] {
		if row.ID == id {
			copy := row
			chosen = &copy
			break
		}
	}
	if chosen == nil {
		return code, nil, true, fmt.Errorf("commerce: unknown cash bonus group=%d contents=%d bonus=%d", groupID, contents, id)
	}
	n, err := s.counts.LifetimePurchaseTotal(s.groups[group])
	if err != nil {
		return code, nil, true, err
	}
	if n < chosen.RequireCount {
		return code, nil, true, fmt.Errorf("commerce: cash bonus purchase threshold not reached group=%d contents=%d bonus=%d count=%d require=%d", groupID, contents, id, n, chosen.RequireCount)
	}
	claimID := bonusClaimID(group, id)
	var bundle []byte
	if !v.Claims[claimID] {
		bundle, err = s.economy.Apply("cash-bonus-grant:"+claimID, nil, []gamedata.Reward{chosen.Reward})
		if err != nil {
			return code, nil, true, err
		}
		v.Claims[claimID] = true
	}
	response := wire.AppendBytes(nil, 1, bundle)
	response = append(response, s.rewarded(v, group, 2)...)
	v.Receipts[identity] = purchaseReceipt{Digest: digest, Response: response}
	raw, err := json.Marshal(v)
	if err == nil {
		err = s.store.Save("commerce_cash_bonuses", raw)
	}
	return code, response, true, err
}
