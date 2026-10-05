package npcshop

import (
	"bd2server/internal/server/wire"
	"testing"
)

func TestTalentBargainingUsesBoundNPCBasePriceAndSuppressesReputation(t *testing.T) {
	s, e, _ := fixture(t)
	s.design.ShopNPCs = map[uint64][]uint64{71: {8801}}
	calls := 0
	s.SetTalentDiscountSource(func(pack, npc uint64) (uint64, error) {
		if pack != 91 || npc != 8801 {
			t.Fatal("wrong NPC binding", pack, npc)
		}
		calls++
		return 20, nil
	})
	s.SetReputationSource(func(uint64) (uint64, uint64, error) { return 2, 10, nil })
	p := s.design.Products[71][3]
	p.Discount = 40
	p.Premium = 40
	s.design.Products[71][3] = p
	rate := s.rate(p, 71, 1)
	_, b, _, err := s.Handle("/ShopOpen", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	on, _, _ := wire.Varint(b, 2)
	if on != 1 {
		t.Fatal("client bargaining mode not enabled")
	}
	if _, _, _, err = s.Handle("/ShopBuy", buy(2, 1, rate)); err != nil {
		t.Fatal(err)
	}
	if calls == 0 || len(e.costs) != 1 || e.costs[0].Count != 20 {
		t.Fatal("talent price combined with reputation/market", e.costs)
	}
	s.SetTalentDiscountSource(func(uint64, uint64) (uint64, error) { return 0, nil })
	if _, _, _, err = s.Handle("/ShopBuy", buy(2, 1, rate)); err != nil || e.calls != 1 {
		t.Fatal("discount state altered replay price", err, e.calls)
	}
}
func TestBargainingNoBargainAndInvalidDiscountRejectBeforeEconomy(t *testing.T) {
	s, e, _ := fixture(t)
	s.design.ShopNPCs = map[uint64][]uint64{71: {8801}}
	s.SetTalentDiscountSource(func(uint64, uint64) (uint64, error) { return 20, nil })
	p := s.design.Products[71][3]
	p.NoBargain = 1
	s.design.Products[71][3] = p
	if _, _, _, err := s.Handle("/ShopBuy", buy(1, 1, 100)); err == nil || e.calls != 0 {
		t.Fatal("NoBargain bought during talent view")
	}
	s.SetTalentDiscountSource(func(uint64, uint64) (uint64, error) { return 101, nil })
	if _, _, _, err := s.Handle("/ShopBuy", buy(2, 1, 100)); err == nil || e.calls != 0 {
		t.Fatal("invalid discount charged")
	}
}
