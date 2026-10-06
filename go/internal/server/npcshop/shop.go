// Package npcshop implements the ordinary NPC buy/sell market from GameData.
package npcshop

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
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

type Economy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type receipt struct {
	Digest   []byte
	Response []byte
}
type count struct {
	Period string
	Bought uint64
}
type snapshot struct {
	Counts   map[string]count   `json:"counts"`
	Receipts map[string]receipt `json:"receipts"`
}
type Service struct {
	mu             sync.Mutex
	design         gamedata.NPCShopDesign
	store          stateio.Store
	economy        Economy
	items          *player.Inventory
	available      func(uint64) bool
	now            func() time.Time
	session        func() string
	reputation     func(uint64) (uint64, uint64, error)
	talentDiscount func(uint64, uint64) (uint64, error)
	quotedSeed     *uint64
}

func New(d gamedata.NPCShopDesign, store stateio.Store, e Economy, items *player.Inventory, available func(uint64) bool) (*Service, error) {
	if len(d.Shops) == 0 || store == nil || e == nil || items == nil || available == nil {
		return nil, fmt.Errorf("npcshop: invalid configuration")
	}
	s := &Service{design: d, store: store, economy: e, items: items, available: available, now: time.Now}
	_, err := s.load()
	return s, err
}
func (s *Service) SetSessionSource(source func() string) { s.session = source }
func (s *Service) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil && s.session() == id {
		return
	}
	s.session = func() string { return id }
	s.quotedSeed = nil
}

// SetReputationSource supplies the world's persisted pack reputation and
// GameData-derived shop discount. Normal reputation is the initial fallback.
func (s *Service) SetReputationSource(source func(uint64) (uint64, uint64, error)) {
	s.reputation = source
}
func (s *Service) SetTalentDiscountSource(source func(uint64, uint64) (uint64, error)) {
	s.talentDiscount = source
}
func (s *Service) talentDiscountFor(shop gamedata.NPCShop) (uint64, error) {
	if s.talentDiscount == nil {
		return 0, nil
	}
	var best uint64
	for _, npc := range s.design.ShopNPCs[shop.ID] {
		n, e := s.talentDiscount(shop.PackID, npc)
		if e != nil {
			return 0, e
		}
		if n > 100 {
			return 0, fmt.Errorf("npcshop: invalid talent discount")
		}
		if n > best {
			best = n
		}
	}
	return best, nil
}
func (s *Service) reputationFor(pack uint64) (uint64, uint64, error) {
	if s.reputation != nil {
		return s.reputation(pack)
	}
	return 1, 0, nil
}
func (s *Service) load() (snapshot, error) {
	v := snapshot{Counts: map[string]count{}, Receipts: map[string]receipt{}}
	b, e := s.store.Load("npc_shop")
	if e != nil || len(b) == 0 {
		return v, e
	}
	if e = stateio.RequireExactJSONObject(b, "counts", "receipts"); e != nil {
		return v, e
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return v, e
	}
	if v.Counts == nil || v.Receipts == nil {
		return v, fmt.Errorf("npcshop: invalid state")
	}
	return v, nil
}
func (s *Service) save(v snapshot) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.store.Save("npc_shop", b)
}
func key(shop, product uint64) string { return fmt.Sprintf("%d:%d", shop, product) }
func (s *Service) period(shop gamedata.NPCShop) (string, int64) {
	t := s.now().UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch shop.ResetType {
	case 1:
		return day.Format("2006-01-02"), day.AddDate(0, 0, 1).Unix() - t.Unix()
	case 2:
		day = day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		return day.Format("2006-01-02"), day.AddDate(0, 0, 7).Unix() - t.Unix()
	case 3:
		day = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return day.Format("2006-01"), day.AddDate(0, 1, 0).Unix() - t.Unix()
	default:
		return "lifetime", 0
	}
}
func (s *Service) seed() uint64 { return uint64(s.now().UTC().Unix()/86400) % math.MaxInt32 }
func (s *Service) bought(v snapshot, shop, product uint64) uint64 {
	period, _ := s.period(s.design.Shops[shop])
	c := v.Counts[key(shop, product)]
	if c.Period != period {
		return 0
	}
	return c.Bought
}
func (s *Service) shopWire(v snapshot, id uint64) []byte {
	_, remaining := s.period(s.design.Shops[id])
	b := wire.AppendVarint(nil, 1, id)
	if remaining > 0 {
		b = wire.AppendVarint(b, 2, uint64(remaining))
	}
	ids := make([]uint64, 0, len(s.design.Products[id]))
	for n := range s.design.Products[id] {
		ids = append(ids, n)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, n := range ids {
		p := wire.AppendVarint(nil, 1, n)
		p = wire.AppendVarint(p, 2, s.bought(v, id, n))
		b = wire.AppendBytes(b, 3, p)
	}
	return b
}
func (s *Service) allShops(v snapshot, field int) []byte {
	ids := make([]uint64, 0)
	for id, r := range s.design.Shops {
		if s.available(r.PackID) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var b []byte
	for _, id := range ids {
		b = wire.AppendBytes(b, field, s.shopWire(v, id))
	}
	return b
}
func scalar(b []byte, f int) (uint64, error) { v, _, e := wire.Varint(b, f); return v, e }
func messages(b []byte, f int) ([][]byte, error) {
	var out [][]byte
	e := wire.Walk(b, func(v wire.Field) error {
		if v.Number != f {
			return nil
		}
		if v.Type != 2 {
			return fmt.Errorf("npcshop: malformed nested message")
		}
		out = append(out, append([]byte(nil), v.Value...))
		return nil
	})
	return out, e
}
func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/ShopInfo": 53, "/ShopBuy": 54, "/ShopSell": 55, "/ShopOpen": 107, "/ShopReputationInfo": 520}
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	seq, e := scalar(request, 1)
	if e != nil || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, fmt.Errorf("npcshop: invalid sequence")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.load()
	if e != nil {
		return 0, nil, true, e
	}
	if path == "/ShopInfo" {
		b := s.allShops(v, 1)
		seed := s.seed()
		s.quotedSeed = &seed
		return code, wire.AppendVarint(b, 2, seed), true, nil
	}
	if path == "/ShopOpen" {
		var b []byte
		packs := map[uint64]bool{}
		isTalent := false
		for _, r := range s.design.Shops {
			if s.available(r.PackID) {
				discount, e := s.talentDiscountFor(r)
				if e != nil {
					return 0, nil, true, e
				}
				isTalent = isTalent || discount > 0
			}
			if !packs[r.PackID] && s.available(r.PackID) {
				packs[r.PackID] = true
				state, _, err := s.reputationFor(r.PackID)
				if err != nil {
					return 0, nil, true, err
				}
				p := wire.AppendVarint(nil, 1, r.PackID)
				p = wire.AppendVarint(p, 2, state)
				b = wire.AppendBytes(b, 1, p)
			}
		}
		if isTalent {
			b = wire.AppendVarint(b, 2, 1)
		}
		return code, b, true, nil
	}
	if path == "/ShopReputationInfo" {
		pack, err := scalar(request, 2)
		if err != nil || !s.available(pack) {
			return 0, nil, true, fmt.Errorf("npcshop: unavailable reputation pack")
		}
		state, _, err := s.reputationFor(pack)
		if err != nil {
			return 0, nil, true, err
		}
		return code, wire.AppendVarint(nil, 1, state), true, nil
	}
	if s.session == nil || s.session() == "" {
		return 0, nil, true, fmt.Errorf("npcshop: missing session identity")
	}
	identity := "npcshop:" + s.session() + ":" + path + ":" + strconv.FormatUint(seq, 10)
	digest := sha256.Sum256(request)
	if r, exists := v.Receipts[identity]; exists {
		if !bytes.Equal(r.Digest, digest[:]) {
			return 0, nil, true, fmt.Errorf("npcshop: reused sequence")
		}
		return code, r.Response, true, nil
	}
	var costs, rewards []gamedata.Reward
	var sold []player.Item
	changed := map[uint64]bool{}
	if path == "/ShopBuy" {
		costs, rewards, changed, e = s.buy(request, &v)
		if e == nil {
			costs, sold, e = s.purchaseMaterials(request, costs)
		}
	} else {
		sold, rewards, e = s.sell(request)
	}
	if e != nil {
		return 0, nil, true, e
	}
	if len(sold) > 0 {
		if e = s.items.CanConsume(sold); e != nil {
			return 0, nil, true, e
		}
	}
	bundle, e := s.economy.Apply(identity, costs, rewards)
	if e != nil {
		return 0, nil, true, e
	}
	// This and Economy.Apply execute in the enclosing account transaction.
	// Consume the concrete stacks the client removes, rather than letting a
	// type/id-only debit choose a different stack of the same resource.
	if len(sold) > 0 {
		if e = s.items.Consume(sold); e != nil {
			return 0, nil, true, e
		}
	}
	b := wire.AppendBytes(nil, 1, bundle)
	ids := make([]uint64, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		b = wire.AppendBytes(b, 2, s.shopWire(v, id))
	}
	v.Receipts[identity] = receipt{Digest: digest[:], Response: b}
	if e = s.save(v); e != nil {
		return 0, nil, true, e
	}
	return code, b, true, nil
}
func (s *Service) buy(request []byte, v *snapshot) ([]gamedata.Reward, []gamedata.Reward, map[uint64]bool, error) {
	groups, e := messages(request, 2)
	if e != nil || len(groups) == 0 {
		return nil, nil, nil, fmt.Errorf("npcshop: empty purchase")
	}
	var costs, rewards []gamedata.Reward
	changed := map[uint64]bool{}
	seen := map[string]bool{}
	for _, g := range groups {
		shop, e := scalar(g, 1)
		r, exists := s.design.Shops[shop]
		if e != nil || !exists || !s.available(r.PackID) {
			return nil, nil, nil, fmt.Errorf("npcshop: unavailable shop %d", shop)
		}
		state, discount, err := s.reputationFor(r.PackID)
		if err != nil {
			return nil, nil, nil, err
		}
		if state == 0 || discount > 100 {
			return nil, nil, nil, fmt.Errorf("npcshop: unavailable reputation")
		}
		talentDiscount, e := s.talentDiscountFor(r)
		if e != nil {
			return nil, nil, nil, e
		}
		rows, e := messages(g, 2)
		if e != nil || len(rows) == 0 {
			return nil, nil, nil, fmt.Errorf("npcshop: empty product list")
		}
		for _, b := range rows {
			id, _ := scalar(b, 2)
			n, _ := scalar(b, 3)
			inven, _ := scalar(b, 1)
			rate, _ := scalar(b, 4)
			p, ok := s.design.Products[shop][id]
			k := key(shop, id)
			if !ok || n == 0 || n > math.MaxInt32 || inven != 0 || seen[k] {
				return nil, nil, nil, fmt.Errorf("npcshop: invalid product %d", id)
			}
			seen[k] = true
			if p.Reputation > 0 && state != 2 {
				return nil, nil, nil, fmt.Errorf("npcshop: product requires good reputation")
			}
			expected := s.rate(p, shop, 1)
			if rate != expected {
				return nil, nil, nil, fmt.Errorf("npcshop: stale/invalid price rate %d expected %d", rate, expected)
			}
			old := s.bought(*v, shop, id)
			if p.MaxCount != 0 && p.Reward.Type != 12 && (old > p.MaxCount || n > p.MaxCount-old) {
				return nil, nil, nil, fmt.Errorf("npcshop: sold out product %d", id)
			}
			price := uint64(float32(p.Price.Count*expected) / 100)
			if talentDiscount > 0 {
				if p.NoBargain == 1 {
					return nil, nil, nil, fmt.Errorf("npcshop: product excluded during bargaining")
				}
				// ShopUI.RefreshProductsPrice uses the base price for talent
				// bargaining, replacing both market variation and reputation.
				price = uint64(float32(p.Price.Count) * (float32(100-talentDiscount) / 100))
			} else if discount > 0 {
				price = uint64(float32(price*(100-discount)) / 100)
			}
			if price > math.MaxInt32/n || p.Reward.Count > math.MaxInt32/n {
				return nil, nil, nil, fmt.Errorf("npcshop: purchase overflow")
			}
			cost := p.Price
			cost.Count = price * n
			if cost.Count > 0 {
				costs = append(costs, cost)
			}
			reward := p.Reward
			reward.Count *= n
			rewards = append(rewards, reward)
			period, _ := s.period(r)
			v.Counts[k] = count{Period: period, Bought: old + n}
			changed[shop] = true
		}
	}
	return costs, rewards, changed, nil
}
func (s *Service) sell(request []byte) ([]player.Item, []gamedata.Reward, error) {
	id, e := scalar(request, 2)
	shop, exists := s.design.Shops[id]
	if e != nil || !exists || !s.available(shop.PackID) {
		return nil, nil, fmt.Errorf("npcshop: unavailable sell shop")
	}
	rows, e := messages(request, 3)
	if e != nil || len(rows) == 0 {
		return nil, nil, fmt.Errorf("npcshop: empty sale")
	}
	inventory := map[uint64]player.Item{}
	for _, item := range s.items.All() {
		inventory[item.InvenIndex] = item
	}
	var costs []player.Item
	var rewards []gamedata.Reward
	seen := map[uint64]bool{}
	for _, b := range rows {
		values := map[int]uint64{}
		if err := wire.Walk(b, func(f wire.Field) error {
			if f.Number < 1 || f.Number > 4 {
				return nil
			}
			if _, duplicate := values[f.Number]; duplicate || f.Type != 0 {
				return fmt.Errorf("npcshop: malformed sale item field %d", f.Number)
			}
			values[f.Number], _ = binary.Uvarint(f.Value)
			return nil
		}); err != nil {
			return nil, nil, err
		}
		product, n, inven, rate := values[2], values[3], values[1], values[4]
		p, ok := s.design.Sell[product]
		item, owned := inventory[inven]
		if !ok || !p.InventorySellable() || !owned || seen[inven] || inven == 0 || inven > math.MaxInt64 || product > math.MaxInt32 || n == 0 || n > math.MaxInt32 || n > item.Count || p.Reward.Type != item.Type || p.Reward.ID != item.ID || item.KeepFlag != 0 {
			return nil, nil, fmt.Errorf("npcshop: invalid sale item")
		}
		seen[inven] = true
		expected := s.rate(p, id, 2)
		if rate != expected {
			return nil, nil, fmt.Errorf("npcshop: stale/invalid sell rate: shop=%d product=%d rate=%d expected=%d seed=%d", id, product, rate, expected, s.marketSeed())
		}
		if expected > 0 && p.Price.Count > math.MaxInt32/expected {
			return nil, nil, fmt.Errorf("npcshop: sale unit price overflow")
		}
		price := uint64(float32(p.Price.Count*expected) / 100)
		if price > math.MaxInt32/n {
			return nil, nil, fmt.Errorf("npcshop: sale overflow")
		}
		cost := item
		cost.Count = n
		costs = append(costs, cost)
		reward := p.Price
		reward.Count = price * n
		if reward.Count > 0 {
			rewards = append(rewards, reward)
		}
	}
	return costs, rewards, nil
}

// Resource currency is removed from the exact inventory rows named by the
// client after independently deriving the required type/id/count from design.
func (s *Service) purchaseMaterials(request []byte, costs []gamedata.Reward) ([]gamedata.Reward, []player.Item, error) {
	need := map[[2]uint64]uint64{}
	var currency []gamedata.Reward
	for _, c := range costs {
		if c.Type == 8 {
			need[[2]uint64{c.Type, c.ID}] += c.Count
		} else {
			currency = append(currency, c)
		}
	}
	rows, e := messages(request, 3)
	if e != nil {
		return nil, nil, e
	}
	var selected []player.Item
	got := map[[2]uint64]uint64{}
	seen := map[uint64]bool{}
	for _, b := range rows {
		index, _ := scalar(b, 1)
		id, _ := scalar(b, 2)
		typ, _ := scalar(b, 3)
		n, _ := scalar(b, 4)
		if index == 0 || id == 0 || typ != 8 || n == 0 || n > math.MaxInt32 || seen[index] {
			return nil, nil, fmt.Errorf("npcshop: invalid purchase material")
		}
		seen[index] = true
		k := [2]uint64{typ, id}
		got[k] += n
		selected = append(selected, player.Item{InvenIndex: index, ID: id, Type: typ, Count: n})
	}
	if len(got) != len(need) {
		return nil, nil, fmt.Errorf("npcshop: purchase material mismatch")
	}
	for k, n := range need {
		if got[k] != n {
			return nil, nil, fmt.Errorf("npcshop: purchase material count mismatch")
		}
	}
	return currency, selected, nil
}

// Client ShopPacket.GetShopRandomValue uses the first WELL512 value for a
// versioned product and server-supplied daily seed. Client Rate is verified,
// never accepted as the authority for wallet arithmetic.
func (s *Service) rate(p gamedata.NPCProduct, shop, tab uint64) uint64 {
	if p.PremiumPriceType == 1 && p.HighShop == shop && p.HighDay == uint64(s.now().UTC().Day()) {
		return 100 + p.HighPremium
	}
	seed := s.marketSeed() + shop + tab + p.Reward.Type + p.Reward.ID
	a := seed
	b := seed + 13*90
	c := a ^ b ^ (a << 16) ^ (b << 15)
	b = seed + 9*90
	b ^= b >> 11
	a = c ^ b
	// WELL512's mask is the unsigned 32-bit pattern 0xDA442D24. The
	// decompiler displays its signed int32 spelling, -633066204, inside
	// an ulong cast; sign-extending that spelling changes the RNG result.
	d := a ^ ((a << 5) & uint64(0xda442d24))
	a = seed + 15*90
	value := a ^ c ^ d ^ (a << 2) ^ (c << 18) ^ (b << 28)
	lo := 100 - p.Discount
	span := p.Discount + p.Premium
	if span == 0 {
		span = 1
	}
	return lo + value%span
}

// The client keeps ShopRandSeed until the next ShopInfo response. Advancing
// the server clock alone must not change a price already displayed in its UI.
func (s *Service) marketSeed() uint64 {
	if s.quotedSeed != nil {
		return *s.quotedSeed
	}
	return s.seed()
}
