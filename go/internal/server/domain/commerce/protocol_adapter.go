package commerce

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/staticdata"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Service) predecessorBought(ctx command.Context, v purchaseState, key gamedata.CashProductKey) bool {
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
		for _, raw := range s.legacy.PurchaseCountDBInfos(ctx) {
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
func (s *Service) AttachShopSeed(ctx command.Context, seed *readonly.Seed) error {
	if seed == nil {
		return fmt.Errorf("commerce: missing shop seed")
	}
	_, raw, handled, err := seed.Handle(ctx, "/CashShopInfo", wire.AppendVarint(nil, 1, 1))
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

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
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
		infos, err := s.purchaseCountDBInfos(ctx)
		if err != nil {
			return 0, nil, true, err
		}
		var b []byte
		for _, i := range infos {
			b = wire.AppendBytes(b, 1, i)
		}
		return 432, b, true, nil
	case "/CashShopBuy":
		return s.buy(ctx, commandSession, request)
	default:
		return 0, nil, false, nil
	}
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

func (s *Service) buy(ctx command.Context, session string, request []byte) (int, []byte, bool, error) {
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

	v, e := s.load(ctx)
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
		if !s.predecessorBought(ctx, v, l.Key) {
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
			for _, raw := range s.legacy.PurchaseCountDBInfos(ctx) {
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
					costs, err = resolver.NativePurchaseCosts(ctx, costs[0])
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
			special, handled, e = s.delegate(ctx, l.Key, request)
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
			ApplyPurchase(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
		}); ok {
			b, err = delivery.ApplyPurchase(ctx, operation, costs, rewards)
		} else {
			b, err = s.economy.Apply(ctx, operation, costs, rewards)
		}
		if err != nil {
			return 61, nil, true, err
		}
		bundle = append(bundle, b...)
		bundle = append(bundle, special...)
		if s.hook != nil {
			if err = s.hook(ctx, operation, d, l.Count); err != nil {
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
		e = s.store.Save(ctx.State, "commerce", encoded)
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

func (s *Service) purchaseCountDBInfos(ctx command.Context) ([][]byte, error) {

	v, e := s.load(ctx)
	if e != nil {
		return nil, e
	}
	counts := map[gamedata.CashProductKey]uint64{}
	if s.legacy != nil {
		for _, raw := range s.legacy.PurchaseCountDBInfos(ctx) {
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

func (s *Service) PurchaseCountDBInfos(ctx command.Context) [][]byte {
	v, _ := s.purchaseCountDBInfos(ctx)
	return v
}

func (h PackInfoHandler) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/PackInfo" {
		return 0, nil, false, nil
	}
	code, response, handled, err := h.World.Handle(ctx, path, request)
	if err != nil || !handled {
		return code, response, handled, err
	}
	pack, evil, err := h.Claims.RewardDBInfos(ctx)
	if err != nil {
		return code, nil, true, err
	}
	var result []byte
	err = wire.Walk(response, func(field wire.Field) error {
		if field.Number != 3 && field.Number != 4 {
			result = append(result, response[field.Start:field.End]...)
		}
		return nil
	})
	if err != nil {
		return code, nil, true, err
	}
	for _, info := range pack {
		result = wire.AppendBytes(result, 3, info)
	}
	for _, info := range evil {
		result = wire.AppendBytes(result, 4, info)
	}
	return code, result, true, nil
}

// ClaimAndInfo is called by AttendanceHandler inside the account transaction.
// Free progression advances once per observed reset day, and buying premium
// catches up the already earned rows without advancing the login day count.
func (s *LoginPasses) ClaimAndInfo(ctx command.Context, identity string) ([]byte, [][]byte, error) {

	if identity == "" {
		return nil, nil, fmt.Errorf("commerce: missing login-pass identity")
	}
	v, err := s.load(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]uint64, 0, len(s.design.Groups))
	for group := range s.design.Groups {
		keys = append(keys, group)
	}
	slices.Sort(keys)
	r, seen := v.Receipts[identity]
	if !seen {
		r = loginPassReceipt{Rewarded: map[uint64]bool{}}
		var rewards []gamedata.Reward
		day := s.now().UTC().Add(-time.Duration(s.resetSeconds) * time.Second).Format("2006-01-02")
		for _, group := range keys {
			if !s.available(ctx, group) {
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
			if s.paid(ctx, rows[0].TicketID) {
				for p.Premium < p.Free {
					rewards = append(rewards, rows[p.Premium].Premium)
					p.Premium++
					r.Rewarded[group] = true
				}
			}
			v.Progress[group] = p
		}
		if len(rewards) > 0 {
			r.Bundle, err = s.economy.Apply(ctx, "login-pass:"+identity, nil, rewards)
			if err != nil {
				return nil, nil, err
			}
		}
		v.Receipts[identity] = r
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		if err = s.store.Save(ctx.State, "commerce_login_passes", raw); err != nil {
			return nil, nil, err
		}
	}
	var infos [][]byte
	for _, group := range keys {
		if !s.available(ctx, group) {
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

// AttachEventShopSchedules joins event type 15 to EventShopTable and then to
// CashProductTable. Hub IDs are presentation identities, never product groups.
// All scheduled goods are published; purchase handlers enforce their windows.
// Call once before serving sessions.
func (s *Service) AttachEventShopSchedules(design *gamedata.CashCatalog, schedules []events.Schedule) error {
	if design == nil || s.shopWindows == nil {
		return fmt.Errorf("commerce: missing event shop catalog or shop seed")
	}
	shops := map[uint64]uint64{}
	groups := map[uint64]bool{}
	for _, shop := range design.EventShops {
		if shop.ID == 0 || shop.ProductGroupID == 0 || shops[shop.ID] != 0 || groups[shop.ProductGroupID] {
			return fmt.Errorf("commerce: invalid or duplicate event shop identity")
		}
		shops[shop.ID], groups[shop.ProductGroupID] = shop.ProductGroupID, true
	}
	windows := map[gamedata.CashProductKey][][2]uint64{}
	var products [][]byte
	for _, raw := range s.shopProducts {
		group, _, _ := wire.Varint(raw, 1)
		if !groups[group] {
			products = append(products, raw)
		}
	}
	rows := append([]events.Schedule(nil), schedules...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].UID < rows[j].UID })
	for _, row := range rows {
		if row.Type != 15 {
			continue
		}
		group := shops[row.ID]
		if group == 0 || row.UID == 0 || row.Start <= 0 || row.Start >= row.End {
			return fmt.Errorf("commerce: invalid event shop schedule %d", row.UID)
		}
		found := false
		start, end := uint64(row.Start), uint64(row.End)
		for _, product := range s.catalog.Designs() {
			key := product.Key
			if key.GroupID != group {
				continue
			}
			found = true
			for _, prior := range windows[key] {
				if start < prior[1] && prior[0] < end {
					return fmt.Errorf("commerce: overlapping event shop product windows")
				}
			}
			windows[key] = append(windows[key], [2]uint64{start, end})
			raw := wire.AppendVarint(nil, 1, key.GroupID)
			raw = wire.AppendVarint(raw, 2, key.ProductID)
			raw = wire.AppendVarint(raw, 3, key.SaleGroup)
			raw = wire.AppendVarint(raw, 4, start)
			raw = wire.AppendVarint(raw, 5, end)
			raw = wire.AppendVarint(raw, 8, row.UID)
			products = append(products, raw)
		}
		if !found {
			return fmt.Errorf("commerce: event shop %d has no products", row.ID)
		}
	}
	s.shopProducts, s.eventShopWindows, s.eventShopGroups = products, windows, groups
	return nil
}

func (e *EntitlementEconomy) Apply(ctx command.Context, identity string, costs, rewards []gamedata.Reward) ([]byte, error) {

	return e.apply(ctx, identity, costs, rewards)
}

func (e *EntitlementEconomy) ApplyResolved(ctx command.Context, identity string, costs, rewards []gamedata.Reward) ([]byte, error) {

	return e.applyPrepared(ctx, identity, costs, rewards, true, nil)
}

func (e *EntitlementEconomy) apply(ctx command.Context, identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	return e.applyPrepared(ctx, identity, costs, rewards, false, nil)
}

func (e *EntitlementEconomy) applyPrepared(ctx command.Context, identity string, costs, rewards []gamedata.Reward, resolved bool, mailed []gamedata.BattleReward) ([]byte, error) {
	if identity == "" {
		return nil, fmt.Errorf("commerce: missing entitlement identity")
	}
	definition, _ := json.Marshal(struct{ Costs, Rewards []gamedata.Reward }{costs, rewards})
	digest := sha256.Sum256(definition)
	s, err := e.load(ctx)
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
		input[i] = gamedata.BattleReward{Type: r.Type, ID: r.ID, Count: r.Count} //nolint:staticcheck // S1016
	}
	leaves := input
	if !resolved {
		leaves, err = e.graph.ResolveGranted(input)
	}
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
		for _, leaf := range append(append([]gamedata.BattleReward(nil), leaves...), mailed...) {
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
	var special []assets.Item
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
			expiry := max(int64(e.items.ContentTicketExpiry(r.ID)), now)
			if r.Count > uint64((math.MaxInt64-expiry)/(30*86400000)) {
				return nil, fmt.Errorf("commerce: subscription expiry overflow")
			}
			expiry += int64(r.Count) * 30 * 86400000
			special = append(special, assets.Item{Type: 19, ID: r.ID, Count: r.Count, ExpiryTime: uint64(expiry), TimeValue: uint64(e.now().UnixMilli())})
			key := strconv.FormatUint(r.ID, 10)
			sub := s.Subscriptions[key]
			if sub.Start == 0 || sub.Expiry <= now {
				sub = cashSubscription{Start: now, Claimed: 1, LastDay: e.day()}
			}
			sub.Expiry = expiry
			s.Subscriptions[key] = sub
		default:
			regular = append(regular, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
			if r.Type == 19 && e.design.TicketTypes[r.ID] == 3 && len(e.design.Attendance[r.ID]) > 0 {
				key := strconv.FormatUint(r.ID, 10)
				if _, exists := s.Subscriptions[key]; !exists {
					s.Subscriptions[key] = cashSubscription{Start: e.now().UnixMilli(), Claimed: 1, LastDay: e.day()}
				}
			}
		}
	}
	bundle, err := e.base.Apply(ctx, identity+":base", costs, regular)
	if err != nil {
		return nil, err
	}
	items, err := e.items.GrantCommerceOnce(ctx, identity+":special", special)
	if err != nil {
		return nil, err
	}
	for _, i := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(i))
		bundle = wire.AppendBytes(bundle, 6, assets.ItemWire(assets.Item{ID: i.ID, Type: i.Type, Count: i.Count}))
	}
	s.Receipts[identity] = entitlementReceipt{Definition: append([]byte(nil), digest[:]...), Bundle: append([]byte(nil), bundle...)}
	if err = e.save(ctx, s); err != nil {
		return nil, err
	}
	return bundle, nil
}

// ClaimSubscriptions grants the next row once per server reset day. Missing
// login days are not retroactively claimed. First-row purchase rewards are
// already present in the product box, matching the client's first-row marker.
func (e *EntitlementEconomy) ClaimSubscriptions(ctx command.Context, identity string) ([]byte, error) {

	if identity == "" {
		return nil, fmt.Errorf("commerce: missing attendance receipt identity")
	}
	s, err := e.load(ctx)
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
		grant, err := e.apply(ctx, fmt.Sprintf("commerce:attendance:%s:%s", key, e.day()), nil, []gamedata.Reward{{Type: r.Reward.Type, ID: r.Reward.ID, Count: r.Reward.Count}})
		if err != nil {
			return nil, err
		}
		bundle = append(bundle, grant...)
		// apply persists its receipt; reload before saving progress so it survives.
		latest, err := e.load(ctx)
		if err != nil {
			return nil, err
		}
		sub.Claimed++
		sub.LastDay = e.day()
		latest.Subscriptions[key] = sub
		if err = e.save(ctx, latest); err != nil {
			return nil, err
		}
		s = latest
	}
	latest, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(receiptKey))
	latest.Receipts[receiptKey] = entitlementReceipt{Definition: append([]byte(nil), digest[:]...), Bundle: append([]byte(nil), bundle...)}
	if err = e.save(ctx, latest); err != nil {
		return nil, err
	}
	return bundle, nil
}

func (e *EntitlementEconomy) MergeAttendance(ctx command.Context, response []byte) ([]byte, error) {

	s, err := e.load(ctx)
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
			n := min(sub.Claimed, uint64(len(e.design.Attendance[ticket])))
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

// ApplyPurchase selects rewards once, activates direct entitlements, and issues
// durable cash mail. All writes run in the enclosing account transaction.
func (e *EntitlementEconomy) ApplyPurchase(ctx command.Context, identity string, costs, rewards []gamedata.Reward) ([]byte, error) {

	if e.mail == nil {
		return nil, fmt.Errorf("commerce: cash mail issuer unavailable")
	}
	definition, _ := json.Marshal(struct{ Costs, Rewards []gamedata.Reward }{costs, rewards})
	digest := sha256.Sum256(definition)
	key := identity + ":delivery"
	state, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	if receipt, ok := state.Receipts[key]; ok {
		if !bytes.Equal(receipt.Definition, digest[:]) {
			return nil, fmt.Errorf("commerce: delivery identity reused")
		}
		return append([]byte(nil), receipt.Bundle...), nil
	}
	resolver, ok := e.graph.(interface {
		ResolveDelivery([]gamedata.BattleReward) (gamedata.CashDelivery, error)
	})
	if !ok {
		return nil, fmt.Errorf("commerce: missing cash delivery resolver")
	}
	input := make([]gamedata.BattleReward, len(rewards))
	for i, r := range rewards {
		input[i] = gamedata.BattleReward(r)
	}
	plan, err := resolver.ResolveDelivery(input)
	if err != nil {
		return nil, err
	}
	direct := make([]gamedata.Reward, len(plan.Direct))
	for i, r := range plan.Direct {
		direct[i] = gamedata.Reward(r)
	}
	var mailed []gamedata.BattleReward
	for _, mail := range plan.Mail {
		mailed = append(mailed, mail.Rewards...)
	}
	bundle, err := e.applyPrepared(ctx, identity+":direct", costs, direct, true, mailed)
	if err != nil {
		return nil, err
	}
	for i, mail := range plan.Mail {
		attachments := make([]gamedata.Reward, len(mail.Rewards))
		for j, r := range mail.Rewards {
			attachments[j] = gamedata.Reward(r)
		}
		if err := e.mail.IssueCashOnce(ctx, fmt.Sprintf("%s:%d", identity, i), mail.TemplateID, attachments, e.now()); err != nil {
			return nil, err
		}
	}
	state, err = e.load(ctx)
	if err != nil {
		return nil, err
	}
	state.Receipts[key] = entitlementReceipt{Definition: digest[:], Bundle: append([]byte(nil), bundle...)}
	if err = e.save(ctx, state); err != nil {
		return nil, err
	}
	return bundle, nil
}

// Both clear-info fields are ordinary proto3 messages, not a oneof. The native
// client creates an empty message for the inactive kind (CommonPacket), and
// omits scalar zero values such as ClearPackagePack and the normal pack level.
func parseClearClaim(request []byte) (clearClaim, error) {
	var scalar [2]uint64
	var rows [2][]byte
	var seen [4]bool
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number < 1 || f.Number > 4 {
			return nil
		}
		if seen[f.Number-1] {
			return fmt.Errorf("commerce: duplicate clear claim field %d", f.Number)
		}
		seen[f.Number-1] = true
		if f.Number <= 2 {
			if f.Type != 0 {
				return fmt.Errorf("commerce: invalid clear claim scalar %d", f.Number)
			}
			scalar[f.Number-1], _ = binary.Uvarint(f.Value)
			if scalar[f.Number-1] > math.MaxInt32 {
				return fmt.Errorf("commerce: clear claim scalar %d exceeds int32", f.Number)
			}
		} else {
			if f.Type != 2 {
				return fmt.Errorf("commerce: invalid clear claim row %d", f.Number)
			}
			rows[f.Number-3] = f.Value
		}
		return nil
	})
	if err != nil {
		return clearClaim{}, err
	}
	if scalar[0] == 0 {
		return clearClaim{}, fmt.Errorf("commerce: invalid clear claim sequence")
	}
	kind := scalar[1]
	if kind > 1 {
		return clearClaim{}, fmt.Errorf("commerce: invalid clear claim type")
	}
	if !seen[kind+2] {
		return clearClaim{}, fmt.Errorf("commerce: missing clear claim row")
	}
	var active [4]uint64
	for i, row := range rows {
		var values [4]uint64
		var fields [4]bool
		if err := wire.Walk(row, func(f wire.Field) error {
			if f.Number < 1 || f.Number > 4 {
				// Unknown active fields retain normal protobuf compatibility. An
				// inactive row must contain only the schema's default scalars.
				if uint64(i) != kind {
					return fmt.Errorf("commerce: nonempty inactive clear claim row")
				}
				return nil
			}
			if fields[f.Number-1] || f.Type != 0 {
				return fmt.Errorf("commerce: invalid clear claim row scalar %d", f.Number)
			}
			fields[f.Number-1] = true
			v, _ := binary.Uvarint(f.Value)
			if v > math.MaxInt32 {
				return fmt.Errorf("commerce: clear claim row scalar %d exceeds int32", f.Number)
			}
			if uint64(i) != kind && v != 0 {
				return fmt.Errorf("commerce: conflicting inactive clear claim row")
			}
			values[f.Number-1] = v
			return nil
		}); err != nil {
			return clearClaim{}, err
		}
		if uint64(i) == kind {
			active = values
		}
	}
	if active[0] == 0 || active[1] == 0 {
		return clearClaim{}, fmt.Errorf("commerce: invalid clear claim identity")
	}
	return clearClaim{kind, active[0], active[1], active[2], active[3]}, nil
}

func (s *ClearPackages) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/ClearPackageReward" {
		return 0, nil, false, nil
	}
	claim, err := parseClearClaim(request)
	if err != nil {
		return 286, nil, true, err
	}
	d, ok := s.design[claim]
	if !ok {
		return 286, nil, true, fmt.Errorf("commerce: unknown clear reward")
	}

	v, err := s.load(ctx)
	if err != nil {
		return 286, nil, true, err
	}
	identity := clearClaimID(claim)
	if receipt, ok := v[identity]; ok {
		return 286, append([]byte(nil), receipt.Response...), true, nil
	}
	if d.Type == 1 && !s.entitled(ctx, d.TicketID) {
		return 286, nil, true, fmt.Errorf("commerce: clear reward premium ticket required")
	}
	proof := s.packCleared
	if claim.Kind == 1 {
		proof = s.towerCleared
	}
	if proof == nil || !proof(d.TargetID, d.Level) {
		return 286, nil, true, fmt.Errorf("commerce: clear reward progression incomplete")
	}
	rewards := []gamedata.Reward{{Type: 9, ID: d.RandomBoxID, Count: 1}}
	var bundle []byte
	if delivery, ok := s.economy.(interface {
		ApplyPurchase(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
	}); ok {
		// Clear reward groups carry MailId just like cash products. The
		// delivery resolver selects the versioned mail template and keeps
		// mailed contents out of the direct inventory response.
		bundle, err = delivery.ApplyPurchase(ctx, identity, nil, rewards)
	} else {
		bundle, err = s.economy.Apply(ctx, identity, nil, rewards)
	}
	if err != nil {
		return 286, nil, true, err
	}
	response := wire.AppendBytes(nil, 1, bundle)
	v[identity] = clearClaimReceipt{Claim: claim, Response: response}
	raw, err := json.Marshal(v)
	if err == nil {
		err = s.store.Save(ctx.State, "commerce_clear_claims", raw)
	}
	return 286, response, true, err
}

// RewardDBInfos is attached to PackInfoResponse fields 3 and 4 so reconnects
// restore claimed reward buttons from server state.
func (s *ClearPackages) RewardDBInfos(ctx command.Context) (pack, evil [][]byte, err error) {

	v, err := s.load(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := v[k].Claim
		b := wire.AppendVarint(nil, 1, c.GroupID)
		b = wire.AppendVarint(b, 2, c.TicketID)
		b = wire.AppendVarint(b, 3, c.TargetID)
		b = wire.AppendVarint(b, 4, c.Level)
		if c.Kind == 0 {
			pack = append(pack, b)
		} else {
			evil = append(evil, b)
		}
	}
	return pack, evil, nil
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

func (s *CashBonuses) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
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

	v, err := s.load(ctx)
	if err != nil {
		return code, nil, true, err
	}
	if path == "/CashBonusInfo" {
		var out []byte
		for _, group := range s.ordered {
			n, err := s.counts.LifetimePurchaseTotal(ctx, s.groups[group])
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
	if commandSession == "" {
		return code, nil, true, fmt.Errorf("commerce: cash bonus session unavailable")
	}
	identity := fmt.Sprintf("cash-bonus:%x:%d", sha256.Sum256([]byte(commandSession)), seq)
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
	n, err := s.counts.LifetimePurchaseTotal(ctx, s.groups[group])
	if err != nil {
		return code, nil, true, err
	}
	if n < chosen.RequireCount {
		return code, nil, true, fmt.Errorf("commerce: cash bonus purchase threshold not reached group=%d contents=%d bonus=%d count=%d require=%d", groupID, contents, id, n, chosen.RequireCount)
	}
	claimID := bonusClaimID(group, id)
	var bundle []byte
	if !v.Claims[claimID] {
		bundle, err = s.economy.Apply(ctx, "cash-bonus-grant:"+claimID, nil, []gamedata.Reward{chosen.Reward})
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
		err = s.store.Save(ctx.State, "commerce_cash_bonuses", raw)
	}
	return code, response, true, err
}

func (h AttendanceHandler) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
	if path != "/Attendance" {
		return 0, nil, false, nil
	}
	if commandSession == "" || h.Events == nil || h.Economy == nil || h.Store == nil {
		return 0, nil, true, fmt.Errorf("commerce: attendance dependencies/session unavailable")
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("commerce: missing attendance sequence")
	}
	code, response, handled, err := h.Events.Handle(ctx, path, request)
	if err != nil || !handled {
		return code, response, handled, err
	}
	// The event handler may already have granted ordinary attendance rewards.
	// The client accepts exactly one reward envelope, so combine every grant in
	// execution order under this wrapper's replay identity.
	response, eventBundle, err := takeAttendanceRewardEnvelope(response)
	if err != nil {
		return code, nil, true, err
	}
	key := fmt.Sprintf("commerce_attendance:%x:%d", sha256.Sum256([]byte(commandSession)), seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	var receipt attendanceReceipt
	previous, err := h.Store.Load(ctx.State, key)
	if err != nil {
		return code, nil, true, err
	}
	var loginBundle []byte
	if h.LoginPasses != nil {
		var infos [][]byte
		loginBundle, infos, err = h.LoginPasses.ClaimAndInfo(ctx, key)
		if err != nil {
			return code, nil, true, err
		}
		for _, info := range infos {
			response = wire.AppendBytes(response, 6, info)
		}
	}
	if previous != nil {
		if err = json.Unmarshal(previous, &receipt); err != nil || receipt.Digest != digest {
			return code, nil, true, fmt.Errorf("commerce: conflicting attendance replay")
		}
	} else {
		receipt.Digest = digest
		subscriptionBundle, err := h.Economy.ClaimSubscriptions(ctx, key)
		if err != nil {
			return code, nil, true, err
		}
		// Preserve events -> login pass -> subscription execution order and all
		// repeated reward entries in the single client envelope.
		receipt.Bundle = append(receipt.Bundle, eventBundle...)
		receipt.Bundle = append(receipt.Bundle, loginBundle...)
		receipt.Bundle = append(receipt.Bundle, subscriptionBundle...)
		raw, err := json.Marshal(receipt)
		if err != nil {
			return code, nil, true, err
		}
		if err = h.Store.Save(ctx.State, key, raw); err != nil {
			return code, nil, true, err
		}
	}
	response, err = h.Economy.MergeAttendance(ctx, response)
	if err != nil {
		return code, nil, true, err
	}
	if len(receipt.Bundle) != 0 {
		response = wire.AppendBytes(response, 1001, receipt.Bundle)
		response = wire.AppendString(response, 1002, key)
	}
	return code, response, true, nil
}

// Strip the child envelope before adding the combined one. Keep native and
// unrelated unknown fields byte-for-byte; reject ambiguous child envelopes
// rather than returning a response the client would silently ignore.
func takeAttendanceRewardEnvelope(response []byte) ([]byte, []byte, error) {
	var native, bundle []byte
	var receipt string
	var hasBundle, hasReceipt bool
	err := wire.Walk(response, func(f wire.Field) error {
		switch f.Number {
		case 1001:
			if f.Type != 2 || hasBundle {
				return fmt.Errorf("commerce: ambiguous attendance reward bundle")
			}
			hasBundle = true
			bundle = append([]byte(nil), f.Value...)
		case 1002:
			if f.Type != 2 || hasReceipt {
				return fmt.Errorf("commerce: ambiguous attendance reward receipt")
			}
			hasReceipt = true
			receipt = string(f.Value)
		default:
			native = append(native, response[f.Start:f.End]...)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if hasBundle != hasReceipt || hasReceipt && (receipt == "" || len(receipt) > 1024) {
		return nil, nil, fmt.Errorf("commerce: incomplete attendance reward envelope")
	}
	return native, bundle, nil
}
