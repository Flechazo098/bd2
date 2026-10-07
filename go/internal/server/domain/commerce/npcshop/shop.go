// Package npcshop implements the ordinary NPC buy/sell market from GameData.
package npcshop

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"math"

	"time"
)

type Economy interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
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
	design    gamedata.NPCShopDesign
	store     stateio.Store
	economy   Economy
	items     *assets.Inventory
	available func(ctx command.Context, _ uint64) bool
	now       func() time.Time

	reputation     func(ctx command.Context, _ uint64) (uint64, uint64, error)
	talentDiscount func(ctx command.Context, _ uint64, _ uint64) (uint64, error)
	quotedSeed     *uint64
}

func New(ctx command.Context, d gamedata.NPCShopDesign, store stateio.Store, e Economy, items *assets.Inventory, available func(ctx command.Context, _ uint64) bool) (*Service, error) {
	if len(d.Shops) == 0 || store == nil || e == nil || items == nil || available == nil {
		return nil, fmt.Errorf("npcshop: invalid configuration")
	}
	s := &Service{design: d, store: store, economy: e, items: items, available: available, now: time.Now}
	_, err := s.load(ctx)
	return s, err
}

func (s *Service) BeginLogin(ctx command.Context) {

	s.quotedSeed = nil
}

// SetReputationSource supplies the world's persisted pack reputation and
// GameData-derived shop discount. Normal reputation is the initial fallback.
func (s *Service) SetReputationSource(source func(ctx command.Context, _ uint64) (uint64, uint64, error)) {
	s.reputation = source
}
func (s *Service) SetTalentDiscountSource(source func(ctx command.Context, _ uint64, _ uint64) (uint64, error)) {
	s.talentDiscount = source
}
func (s *Service) talentDiscountFor(ctx command.Context, shop gamedata.NPCShop) (uint64, error) {
	if s.talentDiscount == nil {
		return 0, nil
	}
	var best uint64
	for _, npc := range s.design.ShopNPCs[shop.ID] {
		n, e := s.talentDiscount(ctx, shop.PackID, npc)
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
func (s *Service) reputationFor(ctx command.Context, pack uint64) (uint64, uint64, error) {
	if s.reputation != nil {
		return s.reputation(ctx, pack)
	}
	return 1, 0, nil
}
func (s *Service) load(ctx command.Context) (snapshot, error) {
	v := snapshot{Counts: map[string]count{}, Receipts: map[string]receipt{}}
	b, e := s.store.Load(ctx.State, "npc_shop")
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
func (s *Service) save(ctx command.Context, v snapshot) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "npc_shop", b)
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
