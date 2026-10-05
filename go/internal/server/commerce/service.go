package commerce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

// Economy and the receipt store must participate in the caller's account
// transaction. A failed hook, delegate or save then rolls back all purchases.
type Economy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type CountProvider interface{ PurchaseCountDBInfos() [][]byte }
type PurchaseDelegate func(gamedata.CashProductKey, []byte) ([]byte, bool, error)
type PurchaseHook func(string, gamedata.CashProductDesign, uint64) error
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
	mu               sync.Mutex
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

func NewService(catalog *Catalog, store stateio.Store, economy Economy) (*Service, error) {
	if catalog == nil || store == nil || economy == nil {
		return nil, fmt.Errorf("commerce: invalid service dependencies")
	}
	s := &Service{catalog: catalog, store: store, economy: economy, now: time.Now}
	_, err := s.load()
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
func (s *Service) predecessorBought(v purchaseState, key gamedata.CashProductKey) bool {
	prior := s.predecessors[key]
	if len(prior) == 0 {
		return true
	}
	for _, p := range prior {
		if v.Counts[skuKey(p)].Lifetime > 0 {
			return true
		}
	}
	if s.legacy != nil {
		for _, raw := range s.legacy.PurchaseCountDBInfos() {
			g, _, _ := wire.Varint(raw, 1)
			id, _, _ := wire.Varint(raw, 2)
			sale, _, _ := wire.Varint(raw, 3)
			n, _, _ := wire.Varint(raw, 4)
			if n > 0 {
				for _, p := range prior {
					if p.GroupID == g && p.ProductID == id && p.SaleGroup == sale {
						return true
					}
				}
			}
		}
	}
	return false
}

// AttachShopSeed retains versioned dynamic windows and event identities. It
// must run before serving sessions; absent windows never authorize timed goods.
func (s *Service) AttachShopSeed(seed *readonly.Seed) error {
	if seed == nil {
		return fmt.Errorf("commerce: missing shop seed")
	}
	_, raw, handled, err := seed.Handle("/CashShopInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil || !handled {
		return fmt.Errorf("commerce: invalid shop seed: %w", err)
	}
	windows := map[gamedata.CashProductKey][2]uint64{}
	var products [][]byte
	err = wire.Walk(raw, func(f wire.Field) error {
		if f.Number != 1 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("commerce: invalid shop product")
		}
		g, _, e := wire.Varint(f.Value, 1)
		if e != nil {
			return e
		}
		id, _, e := wire.Varint(f.Value, 2)
		if e != nil {
			return e
		}
		sale, _, e := wire.Varint(f.Value, 3)
		if e != nil {
			return e
		}
		start, _, e := wire.Varint(f.Value, 4)
		if e != nil {
			return e
		}
		end, _, e := wire.Varint(f.Value, 5)
		if e != nil {
			return e
		}
		k := gamedata.CashProductKey{GroupID: g, ProductID: id, SaleGroup: sale}
		if _, ok := windows[k]; ok {
			return fmt.Errorf("commerce: duplicate shop schedule")
		}
		if end != 0 && start >= end {
			return fmt.Errorf("commerce: invalid shop window")
		}
		windows[k] = [2]uint64{start, end}
		products = append(products, append([]byte(nil), f.Value...))
		return nil
	})
	if err != nil {
		return err
	}
	s.shopProducts = products
	s.shopWindows = windows
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
func (s *Service) IsAvailable(key gamedata.CashProductKey) bool {
	d, ok := s.catalog.Design(key)
	return ok && s.available(d)
}
func (s *Service) shopInfo() []byte {
	var response []byte
	for _, raw := range s.shopProducts {
		g, _, _ := wire.Varint(raw, 1)
		id, _, _ := wire.Varint(raw, 2)
		sale, _, _ := wire.Varint(raw, 3)
		_, ok := s.catalog.Design(gamedata.CashProductKey{GroupID: g, ProductID: id, SaleGroup: sale})
		if ok {
			response = wire.AppendBytes(response, 1, raw)
		}
	}
	t := s.now().UTC().Add(-time.Duration(s.resetSeconds) * time.Second)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	daily := day.AddDate(0, 0, 1)
	days := (8 - int(day.Weekday())) % 7
	if days == 0 {
		days = 7
	}
	weekly := day.AddDate(0, 0, days)
	monthly := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	for i, reset := range []time.Time{daily, weekly, monthly} {
		response = wire.AppendVarint(response, i+2, uint64(reset.Add(time.Duration(s.resetSeconds)*time.Second).UnixMilli()))
	}
	return response
}
func (s *Service) SetClock(now func() time.Time, resetSeconds int64) {
	s.now = now
	s.resetSeconds = resetSeconds
}
func skuKey(k gamedata.CashProductKey) string {
	return fmt.Sprintf("%d:%d:%d", k.GroupID, k.ProductID, k.SaleGroup)
}
func (s *Service) load() (purchaseState, error) {
	v := purchaseState{Receipts: map[string]purchaseReceipt{}, Billing: map[string]string{}, Counts: map[string]purchaseCount{}}
	b, e := s.store.Load("commerce")
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
func (s *Service) HandleSession(path string, request []byte, session string) (int, []byte, bool, error) {
	switch path {
	case "/CashShopInfo":
		if s.shopWindows == nil {
			return 0, nil, false, nil
		}
		if seq, ok, err := wire.Varint(request, 1); err != nil || !ok || seq == 0 || seq > math.MaxInt32 {
			return 60, nil, true, fmt.Errorf("commerce: invalid shop sequence")
		}
		return 60, s.shopInfo(), true, nil
	case "/CashShopPurchaseCountInfo":
		if seq, ok, err := wire.Varint(request, 1); err != nil || !ok || seq == 0 || seq > math.MaxInt32 {
			return 432, nil, true, fmt.Errorf("commerce: invalid count sequence")
		}
		infos, err := s.purchaseCountDBInfos()
		if err != nil {
			return 0, nil, true, err
		}
		var b []byte
		for _, i := range infos {
			b = wire.AppendBytes(b, 1, i)
		}
		return 432, b, true, nil
	case "/CashShopBuy":
		return s.buy(session, request)
	default:
		return 0, nil, false, nil
	}
}
func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	return s.HandleSession(path, request, "")
}

type buyLine struct {
	Key   gamedata.CashProductKey
	Count uint64
}

func parseBuy(request []byte) (uint64, []buyLine, []string, error) {
	seq, found, e := wire.Varint(request, 1)
	if e != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, nil, fmt.Errorf("commerce: invalid sequence")
	}
	group, found, e := wire.Varint(request, 3)
	if e != nil || !found || group == 0 {
		return 0, nil, nil, fmt.Errorf("commerce: invalid product group")
	}
	var lines []buyLine
	var billing []string
	seen := map[gamedata.CashProductKey]bool{}
	e = wire.Walk(request, func(f wire.Field) error {
		switch f.Number {
		case 4:
			if f.Type != 2 {
				return fmt.Errorf("commerce: invalid buy info")
			}
			id, ok, err := wire.Varint(f.Value, 1)
			if err != nil || !ok || id == 0 {
				return fmt.Errorf("commerce: invalid product id")
			}
			sale, _, err := wire.Varint(f.Value, 2)
			if err != nil {
				return err
			}
			n, ok, err := wire.Varint(f.Value, 3)
			if err != nil || !ok || n == 0 || n > math.MaxInt32 {
				return fmt.Errorf("commerce: invalid buy count")
			}
			k := gamedata.CashProductKey{GroupID: group, ProductID: id, SaleGroup: sale}
			if seen[k] {
				return fmt.Errorf("commerce: duplicate buy info")
			}
			seen[k] = true
			lines = append(lines, buyLine{k, n})
		case 7:
			if f.Type != 2 {
				return fmt.Errorf("commerce: invalid billing info")
			}
			return wire.Walk(f.Value, func(b wire.Field) error {
				if b.Number == 1 || b.Number == 2 {
					if b.Type != 2 {
						return fmt.Errorf("commerce: invalid billing identity")
					}
					if len(b.Value) > 16384 {
						return fmt.Errorf("commerce: excessive billing identity")
					}
					if len(b.Value) > 0 {
						h := sha256.Sum256(b.Value)
						billing = append(billing, fmt.Sprintf("%d:%x", b.Number, h))
					}
				}
				return nil
			})
		}
		return nil
	})
	if e == nil && len(lines) == 0 {
		e = fmt.Errorf("commerce: empty purchase")
	}
	return seq, lines, billing, e
}
func (s *Service) buy(session string, request []byte) (int, []byte, bool, error) {
	if session == "" {
		return 61, nil, true, fmt.Errorf("commerce: authenticated session required")
	}
	seq, lines, billing, e := parseBuy(request)
	if e != nil {
		return 61, nil, true, e
	}
	var native, cash bool
	for _, line := range lines {
		if design, ok := s.catalog.Design(line.Key); ok {
			if design.PriceType == 1 {
				cash = true
			} else {
				native = true
			}
		}
	}
	if native && cash {
		return 61, nil, true, fmt.Errorf("commerce: mixed cash and native purchase")
	}
	if !cash {
		// Native BillingInfo contains display identifiers (often a product ID),
		// not a unique payment receipt. Request identity provides replay safety.
		billing = nil
	}
	h := sha256.Sum256(request)
	digest := hex.EncodeToString(h[:])
	sessionHash := sha256.Sum256([]byte(session))
	identity := fmt.Sprintf("commerce:%x:%d", sessionHash, seq)
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.load()
	if e != nil {
		return 61, nil, true, e
	}
	if r, ok := v.Receipts[identity]; ok {
		if r.Digest != digest {
			return 61, nil, true, fmt.Errorf("commerce: sequence reused with different purchase")
		}
		return 61, append([]byte(nil), r.Response...), true, nil
	}
	for _, b := range billing {
		if _, ok := v.Billing[b]; ok {
			return 61, nil, true, fmt.Errorf("commerce: billing receipt already used")
		}
	}
	// Validate every line before charging the first one.
	designs := make([]gamedata.CashProductDesign, len(lines))
	quotes := make([]Product, len(lines))
	for i, l := range lines {
		if !s.predecessorBought(v, l.Key) {
			return 61, nil, true, fmt.Errorf("commerce: preceding package purchase required")
		}
		d, ok := s.catalog.Design(l.Key)
		if !ok {
			return 61, nil, true, fmt.Errorf("commerce: unknown product")
		}
		if !s.available(d) {
			return 61, nil, true, fmt.Errorf("commerce: product is not currently available")
		}
		if s.specialProducts[l.Key] && (len(lines) != 1 || l.Count != 1) {
			return 61, nil, true, fmt.Errorf("commerce: special purchase requires one product")
		}
		var q Product
		if d.PriceType == 1 {
			var err error
			q, err = s.catalog.Quote(l.Key, l.Count)
			if err != nil {
				return 61, nil, true, err
			}
		} else {
			cost, err := nativePrice(d, l.Count)
			if err != nil {
				return 61, nil, true, err
			}
			q = Product{Enabled: true, ItemType: d.PriceType, Cost: cost}
		}
		if !q.Enabled {
			return 61, nil, true, fmt.Errorf("commerce: product disabled")
		}
		if len(lines) > 1 && d.BulkOrderAvailability != 1 {
			return 61, nil, true, fmt.Errorf("commerce: bulk purchase disabled")
		}
		n := s.count(v, d)
		if s.legacy != nil {
			for _, raw := range s.legacy.PurchaseCountDBInfos() {
				g, _, _ := wire.Varint(raw, 1)
				id, _, _ := wire.Varint(raw, 2)
				sale, _, _ := wire.Varint(raw, 3)
				legacyCount, _, _ := wire.Varint(raw, 4)
				if g == l.Key.GroupID && id == l.Key.ProductID && sale == l.Key.SaleGroup && legacyCount > n {
					n = legacyCount
				}
			}
		}
		if d.PurchaseLimitType > 4 {
			return 61, nil, true, fmt.Errorf("commerce: unknown purchase limit")
		}
		if n > math.MaxInt32-l.Count {
			return 61, nil, true, fmt.Errorf("commerce: purchase count overflow")
		}
		if d.PurchaseLimitType != 0 && (d.PurchaseLimitCount == 0 || n+l.Count > d.PurchaseLimitCount) {
			return 61, nil, true, fmt.Errorf("commerce: purchase limit exceeded")
		}
		designs[i] = d
		quotes[i] = q
	}
	if cash {
		e = validateAcceptedQuote(request, quotes)
	}
	if e != nil {
		return 61, nil, true, e
	}
	var bundle []byte
	for i, l := range lines {
		d, q := designs[i], quotes[i]
		operation := fmt.Sprintf("%s:%d", identity, i)
		var costs []gamedata.Reward
		if q.Cost > 0 {
			if d.PriceType != 1 {
				costs = []gamedata.Reward{{Type: d.PriceType, ID: d.PriceID, Count: q.Cost}}
				if resolver, ok := s.economy.(nativeCostResolver); ok {
					var err error
					costs, err = resolver.NativePurchaseCosts(costs[0])
					if err != nil {
						return 61, nil, true, err
					}
				}
			} else {
				var typ uint64
				switch q.Currency {
				case "paid_diamonds":
					typ = 2
				case "diamonds":
					typ = 3
				case "gold":
					typ = 4
				default:
					return 61, nil, true, fmt.Errorf("commerce: unknown currency %q", q.Currency)
				}
				costs = []gamedata.Reward{{Type: typ, Count: q.Cost}}
			}
		}
		var special []byte
		handled := false
		// Delegate must only select known special products. Parent account transaction
		// guarantees its grant and the subsequent debit commit together.
		if s.delegate != nil && (d.PriceType == 1 || s.specialProducts[l.Key]) {
			special, handled, e = s.delegate(l.Key, request)
			if e != nil {
				return 61, nil, true, e
			}
			if handled && (len(lines) != 1 || l.Count != 1) {
				return 61, nil, true, fmt.Errorf("commerce: special purchase requires one product")
			}
		}
		var rewards []gamedata.Reward
		if !handled {
			if d.RandomBoxID == 0 {
				return 61, nil, true, fmt.Errorf("commerce: product reward missing")
			}
			rewards = append(rewards, gamedata.Reward{Type: 9, ID: d.RandomBoxID, Count: l.Count})
			if d.BonusRandomBoxID != 0 {
				rewards = append(rewards, gamedata.Reward{Type: 9, ID: d.BonusRandomBoxID, Count: l.Count})
			}
		}
		var b []byte
		var err error
		if delivery, ok := s.economy.(interface {
			ApplyPurchase(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
		}); ok {
			b, err = delivery.ApplyPurchase(operation, costs, rewards)
		} else {
			b, err = s.economy.Apply(operation, costs, rewards)
		}
		if err != nil {
			return 61, nil, true, err
		}
		bundle = append(bundle, b...)
		bundle = append(bundle, special...)
		if s.hook != nil {
			if err = s.hook(operation, d, l.Count); err != nil {
				return 61, nil, true, err
			}
		}
		old := v.Counts[skuKey(l.Key)]
		if old.Lifetime > math.MaxUint64-l.Count {
			return 61, nil, true, fmt.Errorf("commerce: lifetime purchase count overflow")
		}
		v.Counts[skuKey(l.Key)] = purchaseCount{Count: s.count(v, d) + l.Count, Period: s.period(d.PurchaseLimitType), Lifetime: old.Lifetime + l.Count}
	}
	response := wire.AppendBytes(nil, 1, bundle)
	v.Receipts[identity] = purchaseReceipt{digest, response}
	for _, b := range billing {
		v.Billing[b] = identity
	}
	encoded, e := json.Marshal(v)
	if e == nil {
		e = s.store.Save("commerce", encoded)
	}
	return 61, response, true, e
}

// The receipt repeats the client-visible quote for stale-catalog detection.
// Prices always come from Catalog, and a mismatch never changes the debit.
func validateAcceptedQuote(request []byte, quotes []Product) error {
	var pay, receipt string
	seen := false
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number != 7 {
			return nil
		}
		if seen || f.Type != 2 {
			return fmt.Errorf("commerce: invalid billing info")
		}
		seen = true
		return wire.Walk(f.Value, func(b wire.Field) error {
			if b.Number == 1 {
				if b.Type != 2 || pay != "" {
					return fmt.Errorf("commerce: invalid pay id")
				}
				pay = string(b.Value)
			}
			if b.Number == 2 {
				if b.Type != 2 || receipt != "" {
					return fmt.Errorf("commerce: invalid receipt")
				}
				receipt = string(b.Value)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	parts := strings.Split(receipt, ":")
	if len(parts) != 4 || parts[0] != "bd2-local-commerce-v1" || parts[1] != pay {
		return fmt.Errorf("commerce: local billing quote required")
	}
	payID, err := strconv.ParseUint(pay, 10, 64)
	if err != nil || payID == 0 || strconv.FormatUint(payID, 10) != pay {
		return fmt.Errorf("commerce: invalid local pay id")
	}
	typ, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || strconv.FormatUint(typ, 10) != parts[2] {
		return fmt.Errorf("commerce: invalid accepted quote currency")
	}
	amount, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || strconv.FormatUint(amount, 10) != parts[3] {
		return fmt.Errorf("commerce: invalid accepted quote amount")
	}
	var expected uint64
	for _, q := range quotes {
		if q.ItemType != typ || q.Cost > math.MaxInt32-expected {
			return fmt.Errorf("commerce: accepted quote currency mismatch")
		}
		expected += q.Cost
	}
	if expected != amount {
		return fmt.Errorf("commerce: purchase quote changed; refresh the shop")
	}
	return nil
}
func (s *Service) purchaseCountDBInfos() ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.load()
	if e != nil {
		return nil, e
	}
	counts := map[gamedata.CashProductKey]uint64{}
	if s.legacy != nil {
		for _, raw := range s.legacy.PurchaseCountDBInfos() {
			g, _, _ := wire.Varint(raw, 1)
			id, _, _ := wire.Varint(raw, 2)
			sale, _, _ := wire.Varint(raw, 3)
			n, _, _ := wire.Varint(raw, 4)
			counts[gamedata.CashProductKey{GroupID: g, ProductID: id, SaleGroup: sale}] = n
		}
	}
	for _, d := range s.catalog.Designs() {
		if n := s.count(v, d); n > counts[d.Key] {
			counts[d.Key] = n
		}
	}
	keys := make([]gamedata.CashProductKey, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.ProductID != b.ProductID {
			return a.ProductID < b.ProductID
		}
		return a.SaleGroup < b.SaleGroup
	})
	var out [][]byte
	for _, k := range keys {
		b := wire.AppendVarint(nil, 1, k.GroupID)
		b = wire.AppendVarint(b, 2, k.ProductID)
		b = wire.AppendVarint(b, 3, k.SaleGroup)
		b = wire.AppendVarint(b, 4, counts[k])
		out = append(out, b)
	}
	return out, nil
}
func (s *Service) PurchaseCountDBInfos() [][]byte { v, _ := s.purchaseCountDBInfos(); return v }

// LifetimePurchaseTotal derives cumulative bonuses from committed purchases,
// including previous reset periods, without keeping a second purchase counter.
func (s *Service) LifetimePurchaseTotal(keys []gamedata.CashProductKey) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.load()
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
func (s *Service) HasPurchased(k gamedata.CashProductKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.load()
	if e != nil {
		return false
	}
	return v.Counts[skuKey(k)].Count > 0
}

// ConsumeEntitlement is an authorization callback within the caller's account
// transaction. Each successful cash activation consumes one lifetime purchase.
// It must not be called recursively from the purchase hook or delegate.
func (s *Service) ConsumeEntitlement(k gamedata.CashProductKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.load()
	if err != nil {
		return false
	}
	n := v.Counts[skuKey(k)].Lifetime
	used := map[string]uint64{}
	raw, err := s.store.Load("commerce_pass_receipts")
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
	return s.store.Save("commerce_pass_receipts", raw) == nil
}
