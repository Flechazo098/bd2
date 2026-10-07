package commerce

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"time"
)

// Economy and the receipt store must participate in the caller's account
// transaction. A failed hook, delegate or save then rolls back all purchases.
type Economy interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
}
type CountProvider interface {
	PurchaseCountDBInfos(ctx command.Context) [][]byte
}
type PurchaseDelegate func(command.Context, gamedata.CashProductKey, []byte) ([]byte, bool, error)
type PurchaseHook func(command.Context, string, gamedata.CashProductDesign, uint64) error
type purchaseReceipt struct {
	Digest   string `json:"digest"`
	Response []byte `json:"response"`
}
type purchaseCount struct {
	Count    uint64 `json:"count"`
	Period   string `json:"period"`
	Lifetime uint64 `json:"lifetime"`
}
type purchaseState struct {
	Receipts map[string]purchaseReceipt `json:"receipts"`
	Billing  map[string]string          `json:"billing"`
	Counts   map[string]purchaseCount   `json:"counts"`
}
type Service struct {
	catalog          *Catalog
	store            stateio.Store
	economy          Economy
	now              func() time.Time
	resetSeconds     int64
	delegate         PurchaseDelegate
	specialProducts  map[gamedata.CashProductKey]bool
	hook             PurchaseHook
	legacy           CountProvider
	shopProducts     [][]byte
	shopWindows      map[gamedata.CashProductKey][2]uint64
	eventShopWindows map[gamedata.CashProductKey][][2]uint64
	eventShopGroups  map[uint64]bool
	predecessors     map[gamedata.CashProductKey][]gamedata.CashProductKey
}

func NewService(ctx command.Context, catalog *Catalog, store stateio.Store, economy Economy) (*Service, error) {
	if catalog == nil || store == nil || economy == nil {
		return nil, fmt.Errorf("commerce: invalid service dependencies")
	}
	s := &Service{catalog: catalog, store: store, economy: economy, now: time.Now}
	_, err := s.load(ctx)
	return s, err
}
func (s *Service) AttachDelegate(d PurchaseDelegate)  { s.delegate = d }
func (s *Service) AttachPurchaseHook(h PurchaseHook)  { s.hook = h }
func (s *Service) AttachLegacyCounts(p CountProvider) { s.legacy = p }

// AttachPackageRules enforces the client's type-2/type-8 step and relay
// ordering using versioned contentsGroupId/contentsSortId, never SKU numbers.
func (s *Service) AttachPackageRules(packages []gamedata.CashPackageDesign) error {
	rules := map[gamedata.CashProductKey][]gamedata.CashProductKey{}
	for _, p := range packages {
		if (p.PackageType != 2 && p.PackageType != 8) || p.ContentsGroupID == 0 || p.ContentsSortID <= 1 {
			continue
		}
		key := gamedata.CashProductKey{GroupID: p.GroupID, ProductID: p.ID, SaleGroup: p.SaleGroup}
		var prior []gamedata.CashProductKey
		for _, q := range packages {
			if q.PackageType == p.PackageType && q.ContentsGroupID == p.ContentsGroupID && q.ContentsSortID == p.ContentsSortID-1 {
				prior = append(prior, gamedata.CashProductKey{GroupID: q.GroupID, ProductID: q.ID, SaleGroup: q.SaleGroup})
			}
		}
		// Some current-version packages have a sort label above one but an
		// independent contents group. Only a real same-group predecessor is a
		// progression rule; do not invent links between adjacent product IDs.
		if len(prior) == 0 {
			continue
		}
		rules[key] = prior
	}
	s.predecessors = rules
	return nil
}

func (s *Service) available(d gamedata.CashProductDesign) bool {
	if s.eventShopGroups[d.Key.GroupID] {
		now := uint64(s.now().UnixMilli())
		for _, w := range s.eventShopWindows[d.Key] {
			if w[0] <= now && now < w[1] {
				return true
			}
		}
		return false
	}
	w, ok := s.shopWindows[d.Key]
	if !ok {
		return d.TimeLimitType == 0
	}
	now := uint64(s.now().UnixMilli())
	return w[0] <= now && (w[1] == 0 || now < w[1])
}
func (s *Service) IsAvailable(ctx command.Context, key gamedata.CashProductKey) bool {
	d, ok := s.catalog.Design(key)
	return ok && s.available(d)
}

func (s *Service) SetClock(now func() time.Time, resetSeconds int64) {
	s.now = now
	s.resetSeconds = resetSeconds
}
func skuKey(k gamedata.CashProductKey) string {
	return fmt.Sprintf("%d:%d:%d", k.GroupID, k.ProductID, k.SaleGroup)
}
func (s *Service) load(ctx command.Context) (purchaseState, error) {
	v := purchaseState{Receipts: map[string]purchaseReceipt{}, Billing: map[string]string{}, Counts: map[string]purchaseCount{}}
	b, e := s.store.Load(ctx.State, "commerce")
	if e != nil || b == nil {
		return v, e
	}
	if e = stateio.RequireExactJSONObject(b, "receipts", "billing", "counts"); e != nil {
		return v, e
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return v, e
	}
	if v.Receipts == nil || v.Billing == nil || v.Counts == nil {
		return v, fmt.Errorf("commerce: invalid state")
	}
	for identity, receipt := range v.Receipts {
		digest, err := hex.DecodeString(receipt.Digest)
		if identity == "" || err != nil || len(digest) != sha256.Size || len(receipt.Response) == 0 {
			return v, fmt.Errorf("commerce: invalid saved receipt")
		}
	}
	for _, identity := range v.Billing {
		if _, ok := v.Receipts[identity]; !ok {
			return v, fmt.Errorf("commerce: billing identity has no purchase receipt")
		}
	}
	for key, count := range v.Counts {
		if key == "" || count.Period == "" || count.Count == 0 || count.Count > math.MaxInt32 || count.Lifetime < count.Count {
			return v, fmt.Errorf("commerce: invalid saved purchase count")
		}
	}
	return v, nil
}
func (s *Service) period(typ uint64) string {
	t := s.now().UTC().Add(-time.Duration(s.resetSeconds) * time.Second)
	switch typ {
	case 1:
		return t.Format("2006-01-02")
	case 2:
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	case 3:
		return t.Format("2006-01")
	default:
		return "account"
	}
}
func (s *Service) count(v purchaseState, d gamedata.CashProductDesign) uint64 {
	c := v.Counts[skuKey(d.Key)]
	if c.Period != s.period(d.PurchaseLimitType) {
		return 0
	}
	return c.Count
}

type buyLine struct {
	Key   gamedata.CashProductKey
	Count uint64
}

// LifetimePurchaseTotal derives cumulative bonuses from committed purchases,
// including previous reset periods, without keeping a second purchase counter.
func (s *Service) LifetimePurchaseTotal(ctx command.Context, keys []gamedata.CashProductKey) (uint64, error) {

	v, err := s.load(ctx)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, k := range keys {
		n := v.Counts[skuKey(k)].Lifetime
		if n > math.MaxInt32-total {
			return 0, fmt.Errorf("commerce: bonus purchase count overflow")
		}
		total += n
	}
	return total, nil
}
func (s *Service) HasPurchased(ctx command.Context, k gamedata.CashProductKey) bool {

	v, e := s.load(ctx)
	if e != nil {
		return false
	}
	return v.Counts[skuKey(k)].Count > 0
}

// ConsumeEntitlement is an authorization callback within the caller's account
// transaction. Each successful cash activation consumes one lifetime purchase.
// It must not be called recursively from the purchase hook or delegate.
func (s *Service) ConsumeEntitlement(ctx command.Context, k gamedata.CashProductKey) bool {

	v, err := s.load(ctx)
	if err != nil {
		return false
	}
	n := v.Counts[skuKey(k)].Lifetime
	used := map[string]uint64{}
	raw, err := s.store.Load(ctx.State, "commerce_pass_receipts")
	if err != nil {
		return false
	}
	if raw != nil {
		if err = json.Unmarshal(raw, &used); err != nil || used == nil {
			return false
		}
	}
	key := skuKey(k)
	if used[key] >= n {
		return false
	}
	used[key]++
	raw, err = json.Marshal(used)
	if err != nil {
		return false
	}
	return s.store.Save(ctx.State, "commerce_pass_receipts", raw) == nil
}
