package npcshop

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strconv"
)

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
	slices.Sort(ids)
	for _, n := range ids {
		p := wire.AppendVarint(nil, 1, n)
		p = wire.AppendVarint(p, 2, s.bought(v, id, n))
		b = wire.AppendBytes(b, 3, p)
	}
	return b
}

func (s *Service) allShops(ctx command.Context, v snapshot, field int) []byte {
	ids := make([]uint64, 0)
	for id, r := range s.design.Shops {
		if s.available(ctx, r.PackID) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
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

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/ShopInfo": 53, "/ShopBuy": 54, "/ShopSell": 55, "/ShopOpen": 107, "/ShopReputationInfo": 520}
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	seq, e := scalar(request, 1)
	if e != nil || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, fmt.Errorf("npcshop: invalid sequence")
	}

	v, e := s.load(ctx)
	if e != nil {
		return 0, nil, true, e
	}
	if path == "/ShopInfo" {
		b := s.allShops(ctx, v, 1)
		seed := s.seed()
		s.quotedSeed = &seed
		return code, wire.AppendVarint(b, 2, seed), true, nil
	}
	if path == "/ShopOpen" {
		var b []byte
		packs := map[uint64]bool{}
		isTalent := false
		for _, r := range s.design.Shops {
			if s.available(ctx, r.PackID) {
				discount, e := s.talentDiscountFor(ctx, r)
				if e != nil {
					return 0, nil, true, e
				}
				isTalent = isTalent || discount > 0
			}
			if !packs[r.PackID] && s.available(ctx, r.PackID) {
				packs[r.PackID] = true
				state, _, err := s.reputationFor(ctx, r.PackID)
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
		if err != nil || !s.available(ctx, pack) {
			return 0, nil, true, fmt.Errorf("npcshop: unavailable reputation pack")
		}
		state, _, err := s.reputationFor(ctx, pack)
		if err != nil {
			return 0, nil, true, err
		}
		return code, wire.AppendVarint(nil, 1, state), true, nil
	}
	if ctx.SessionID == "" {
		return 0, nil, true, fmt.Errorf("npcshop: missing session identity")
	}
	identity := "npcshop:" + ctx.SessionID + ":" + path + ":" + strconv.FormatUint(seq, 10)
	digest := sha256.Sum256(request)
	if r, exists := v.Receipts[identity]; exists {
		if !bytes.Equal(r.Digest, digest[:]) {
			return 0, nil, true, fmt.Errorf("npcshop: reused sequence")
		}
		return code, r.Response, true, nil
	}
	var costs, rewards []gamedata.Reward
	var sold []assets.Item
	changed := map[uint64]bool{}
	if path == "/ShopBuy" {
		costs, rewards, changed, e = s.buy(ctx, request, &v)
		if e == nil {
			costs, sold, e = s.purchaseMaterials(request, costs)
		}
	} else {
		sold, rewards, e = s.sell(ctx, request)
	}
	if e != nil {
		return 0, nil, true, e
	}
	if len(sold) > 0 {
		if e = s.items.CanConsume(ctx, sold); e != nil {
			return 0, nil, true, e
		}
	}
	bundle, e := s.economy.Apply(ctx, identity, costs, rewards)
	if e != nil {
		return 0, nil, true, e
	}
	// This and Economy.Apply execute in the enclosing account transaction.
	// Consume the concrete stacks the client removes, rather than letting a
	// type/id-only debit choose a different stack of the same resource.
	if len(sold) > 0 {
		if e = s.items.Consume(ctx, sold); e != nil {
			return 0, nil, true, e
		}
	}
	b := wire.AppendBytes(nil, 1, bundle)
	ids := make([]uint64, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		b = wire.AppendBytes(b, 2, s.shopWire(v, id))
	}
	v.Receipts[identity] = receipt{Digest: digest[:], Response: b}
	if e = s.save(ctx, v); e != nil {
		return 0, nil, true, e
	}
	return code, b, true, nil
}

func (s *Service) buy(ctx command.Context, request []byte, v *snapshot) ([]gamedata.Reward, []gamedata.Reward, map[uint64]bool, error) {
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
		if e != nil || !exists || !s.available(ctx, r.PackID) {
			return nil, nil, nil, fmt.Errorf("npcshop: unavailable shop %d", shop)
		}
		state, discount, err := s.reputationFor(ctx, r.PackID)
		if err != nil {
			return nil, nil, nil, err
		}
		if state == 0 || discount > 100 {
			return nil, nil, nil, fmt.Errorf("npcshop: unavailable reputation")
		}
		talentDiscount, e := s.talentDiscountFor(ctx, r)
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

func (s *Service) sell(ctx command.Context, request []byte) ([]assets.Item, []gamedata.Reward, error) {
	id, e := scalar(request, 2)
	shop, exists := s.design.Shops[id]
	if e != nil || !exists || !s.available(ctx, shop.PackID) {
		return nil, nil, fmt.Errorf("npcshop: unavailable sell shop")
	}
	rows, e := messages(request, 3)
	if e != nil || len(rows) == 0 {
		return nil, nil, fmt.Errorf("npcshop: empty sale")
	}
	inventory := map[uint64]assets.Item{}
	for _, item := range s.items.All(ctx) {
		inventory[item.InvenIndex] = item
	}
	var costs []assets.Item
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
func (s *Service) purchaseMaterials(request []byte, costs []gamedata.Reward) ([]gamedata.Reward, []assets.Item, error) {
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
	var selected []assets.Item
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
		selected = append(selected, assets.Item{InvenIndex: index, ID: id, Type: typ, Count: n})
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
