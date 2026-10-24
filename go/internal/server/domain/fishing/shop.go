package fishing

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"
)

func (s *Service) purchaseKey(row []byte) string {
	now := s.now().UTC()
	period := "account"
	switch num(row, 10) {
	case 0:
		period = "all"
	case 1:
		period = now.Format("2006-01-02")
	case 2:
		y, w := now.ISOWeek()
		period = fmt.Sprintf("%d-W%d", y, w)
	case 3:
		period = now.Format("2006-01")
	}
	return fmt.Sprintf("%d:%d:%s", num(row, 1), num(row, 2), period)
}
func (s *Service) shop(ctx command.Context, path string, req []byte, n *snapshot, key string) ([]byte, error) {
	if n.Purchases == nil {
		n.Purchases = map[string]uint64{}
	}
	if path == "/FishingShopBuyInfo" {
		var out []byte
		for _, r := range s.design.Tables["FishingBuyItemTable"] {
			count := n.Purchases[s.purchaseKey(r)]
			if count == 0 {
				continue
			}
			b := wire.AppendVarint(nil, 1, num(r, 1))
			b = wire.AppendVarint(b, 2, num(r, 2))
			b = wire.AppendVarint(b, 3, count)
			out = wire.AppendBytes(out, 1, b)
		}
		now := s.now().UTC()
		day := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		out = wire.AppendVarint(out, 2, uint64(day.Unix()))
		days := (8 - int(now.Weekday())) % 7
		if days == 0 {
			days = 7
		}
		week := time.Date(now.Year(), now.Month(), now.Day()+days, 0, 0, 0, 0, time.UTC)
		out = wire.AppendVarint(out, 3, uint64(week.Unix()))
		month := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return wire.AppendVarint(out, 4, uint64(month.Unix())), nil
	}
	if path == "/FishingShopBuy" {
		var r []byte
		for _, row := range s.design.Rows("FishingBuyItemTable", 1, num(req, 2)) {
			if num(row, 2) == num(req, 3) {
				r = row
				break
			}
		}
		if r == nil {
			return nil, fmt.Errorf("fishing: shop item missing")
		}
		count := num(req, 4)
		if count == 0 || count > math.MaxInt32 || num(r, 3) > math.MaxInt32/count || num(r, 6) > math.MaxInt32/count {
			return nil, fmt.Errorf("fishing: invalid buy quantity")
		}
		pk := s.purchaseKey(r)
		limit := num(r, 9)
		if limit > 0 && (n.Purchases[pk] > limit || count > limit-n.Purchases[pk]) {
			return nil, fmt.Errorf("fishing: shop purchase limit")
		}
		typ, id, quantity := num(r, 5), num(r, 4), num(r, 3)*count
		if typ != 57 && typ != 58 {
			return nil, fmt.Errorf("fishing: unsupported shop item type %d", typ)
		}
		stack := uint64(1)
		if typ == 57 {
			if _, e := s.design.Row("FishingRodTable", 5, id); e != nil {
				return nil, e
			}
			if uint64(len(n.Rods)) > n.RodSlots || quantity > n.RodSlots-uint64(len(n.Rods)) {
				return nil, fmt.Errorf("fishing: rod inventory is full")
			}
		} else {
			item, e := s.design.Row("FishingItemTable", 4, id)
			if e != nil {
				return nil, e
			}
			stack = num(item, 6)
			if stack == 0 {
				return nil, fmt.Errorf("fishing: invalid item stack size")
			}
			remaining := quantity
			for _, it := range n.Baits {
				if it.ID == id && it.Count < stack {
					remaining -= min(remaining, stack-it.Count)
				}
			}
			slots := remaining / stack
			if remaining%stack != 0 {
				slots++
			}
			if uint64(len(n.Baits)) > n.ItemSlots || slots > n.ItemSlots-uint64(len(n.Baits)) {
				return nil, fmt.Errorf("fishing: bait inventory is full")
			}
		}
		bundle, e := s.economy.Apply(ctx, "fishing:"+key, []gamedata.Reward{{Type: num(r, 8), ID: num(r, 7), Count: num(r, 6) * count}}, nil)
		if e != nil {
			return nil, e
		}
		// McFishingShopBuy names the FishingBuyItemTable shop and the bought item.
		if e = s.recordProgress(ctx, conditions.Event{Type: conditions.FishingShopBuy, Sub: num(req, 2), Param: id, Count: count}); e != nil {
			return nil, e
		}
		if typ == 57 {
			for range quantity {
				it := Item{Index: n.Next, ID: id, Count: 1, Time: s.now().Unix()}
				n.Next++
				n.Rods = append(n.Rods, it)
				b := wire.AppendVarint(nil, 1, it.Index)
				b = wire.AppendVarint(b, 2, id)
				b = wire.AppendVarint(b, 3, 57)
				b = wire.AppendVarint(b, 4, 1)
				bundle = wire.AppendBytes(bundle, 1, b)
			}
		} else {
			add := func(index, count uint64) {
				b := wire.AppendVarint(nil, 1, index)
				b = wire.AppendVarint(b, 2, id)
				b = wire.AppendVarint(b, 3, 58)
				b = wire.AppendVarint(b, 4, count)
				bundle = wire.AppendBytes(bundle, 1, b)
			}
			for i := range n.Baits {
				it := &n.Baits[i]
				if it.ID == id && it.Count < stack {
					count := min(quantity, stack-it.Count)
					if count > 0 {
						it.Count += count
						quantity -= count
						add(it.Index, count)
					}
				}
			}
			for quantity > 0 {
				count := min(quantity, stack)
				it := Item{Index: n.Next, ID: id, Count: count, Time: s.now().Unix()}
				n.Next++
				n.Baits = append(n.Baits, it)
				quantity -= count
				add(it.Index, count)
			}
		}
		n.Purchases[pk] += count
		return wire.AppendBytes(nil, 1, bundle), nil
	}
	var rewards []gamedata.Reward
	seen := map[uint64]bool{}
	var removed []uint64
	err := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, f.Number, f.Type)
		}
		index, group, id, count := num(f.Value, 1), num(f.Value, 2), num(f.Value, 3), num(f.Value, 4)
		if seen[index] || count != 1 {
			return fmt.Errorf("fishing: duplicate or invalid sale")
		}
		seen[index] = true
		var fish *Item
		for i := range n.Fish {
			if n.Fish[i].Index == index {
				fish = &n.Fish[i]
				break
			}
		}
		if fish == nil || fish.Locked {
			return fmt.Errorf("fishing: fish unavailable")
		}
		var r []byte
		for _, row := range s.design.Rows("FishingSellItemTable", 1, group) {
			if num(row, 2) == id && num(row, 4) == 56 && num(row, 3) == fish.ID {
				r = row
				break
			}
		}
		if r == nil {
			return fmt.Errorf("fishing: sale differs from installed shop")
		}
		price, e := s.fishPrice(*fish, num(r, 5))
		if e != nil {
			return e
		}
		rewards = append(rewards, gamedata.Reward{Type: num(r, 7), ID: num(r, 6), Count: price})
		removed = append(removed, index)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(removed) == 0 {
		return nil, fmt.Errorf("fishing: empty sale")
	}
	bundle, err := s.economy.Apply(ctx, "fishing:"+key, nil, rewards)
	if err != nil {
		return nil, err
	}
	n.Fish = deleteItems(n.Fish, seen)
	for _, index := range removed {
		bundle = wire.AppendBytes(bundle, 11, fishWire(Item{Index: index}))
	}
	return wire.AppendBytes(nil, 1, bundle), nil
}
func deleteItems(items []Item, seen map[uint64]bool) []Item {
	out := items[:0]
	for _, it := range items {
		if !seen[it.Index] {
			out = append(out, it)
		}
	}
	return out
}
func (s *Service) fishPrice(fish Item, base uint64) (uint64, error) {
	r, e := s.design.Row("FishingFishTable", 12, fish.ID)
	if e != nil {
		return 0, e
	}
	span := dbl(r, 14) - dbl(r, 15)
	if span <= 0 {
		return 0, fmt.Errorf("fishing: invalid fish size span")
	}
	ratio := math.Round((float64(fish.Size) - dbl(r, 15)) / span * 1000)
	d, e := s.design.Row("FishingPricePremiumDefaultTable", 1, 0)
	if e != nil {
		return 0, e
	}
	lo, e := ints(d, 3)
	if e != nil {
		return 0, e
	}
	hi, e := ints(d, 2)
	if e != nil {
		return 0, e
	}
	var premiums []float64
	e = wire.Walk(d, func(f wire.Field) error {
		if f.Number != 4 {
			return nil
		}
		switch f.Type {
		case 2:
			if len(f.Value)%4 != 0 {
				return fmt.Errorf("%w: packed float field %d is %d bytes", wire.ErrMalformed, f.Number, len(f.Value))
			}
			for i := 0; i < len(f.Value); i += 4 {
				premiums = append(premiums, float64(math.Float32frombits(binary.LittleEndian.Uint32(f.Value[i:]))))
			}
		case 5:
			premiums = append(premiums, float64(math.Float32frombits(binary.LittleEndian.Uint32(f.Value))))
		default:
			return fmt.Errorf("%w: float field %d has wire type %d", wire.ErrMalformed, f.Number, f.Type)
		}
		return nil
	})
	if e != nil || len(lo) != len(hi) || len(lo) != len(premiums) {
		return 0, fmt.Errorf("fishing: invalid premium table")
	}
	premium := float64(1)
	for i := range lo {
		if ratio >= float64(lo[i]) && ratio <= float64(hi[i]) {
			premium = premiums[i]
			break
		}
	}
	if !isFinite(premium) || premium < 0 {
		return 0, fmt.Errorf("fishing: invalid price premium")
	}
	decimal, ok := new(big.Rat).SetString(strconv.FormatFloat(premium, 'g', 7, 32))
	if !ok {
		return 0, fmt.Errorf("fishing: invalid decimal premium")
	}
	decimal.Mul(decimal, new(big.Rat).SetInt(new(big.Int).SetUint64(base)))
	price, remain := new(big.Int), new(big.Int)
	price.QuoRem(decimal.Num(), decimal.Denom(), remain)
	if remain.Lsh(remain, 1).Cmp(decimal.Denom()) >= 0 {
		price.Add(price, big.NewInt(1))
	}
	if !price.IsUint64() || price.Uint64() > math.MaxInt32 {
		return 0, fmt.Errorf("fishing: price overflow")
	}
	return price.Uint64(), nil
}
