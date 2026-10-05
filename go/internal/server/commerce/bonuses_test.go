package commerce

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
)

func bonusRequest(seq, group, contents, bonus uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, group)
	b = wire.AppendVarint(b, 3, contents)
	return wire.AppendVarint(b, 4, bonus)
}

func TestCashBonusUsesCommittedSeriesPurchasesAndNativeClaimProtocol(t *testing.T) {
	shop, eco, store := serviceFixture(t, 1)
	packages := []gamedata.CashPackageDesign{
		{GroupID: 2, ID: 1, PackageType: 8, ContentsGroupID: 7},
		{GroupID: 2, ID: 1, SaleGroup: 1, PackageType: 8, ContentsGroupID: 7},
		// Another package kind can reuse contents=7 but must not count.
		{GroupID: 1, ID: 1, PackageType: 3, ContentsGroupID: 7},
	}
	design := &gamedata.CashBonusCatalog{Groups: map[uint64][]gamedata.CashBonusReward{
		7: {{ID: 1, RequireCount: 2, Reward: gamedata.Reward{Type: 8, ID: 1000, Count: 2}}, {ID: 2, RequireCount: 3, Reward: gamedata.Reward{Type: 8, ID: 1000, Count: 15}}},
	}}
	s, err := NewCashBonuses(store, eco, shop, design, packages)
	if err != nil {
		t.Fatal(err)
	}
	infoRequest := wire.AppendVarint(nil, 1, 1)
	if code, info, handled, err := s.Handle("/CashBonusInfo", infoRequest); err != nil || code != 588 || !handled || len(info) != 0 {
		t.Fatal("new account bonus info must be empty", code, info, err)
	}
	claim := bonusRequest(1, 2, 7, 1)
	if _, _, _, err := s.HandleSession("/CashBonusReward", claim, "s"); err == nil || eco.calls != 0 {
		t.Fatal("unearned bonus granted")
	}
	for seq, sale := range []uint64{0, 1} {
		if _, _, _, err := shop.HandleSession("/CashShopBuy", buyRequest(uint64(seq+1), 2, 1, sale, ""), "s"); err != nil {
			t.Fatal(err)
		}
	}
	// Normal recharge is not a member of the bonus series.
	if _, _, _, err := shop.HandleSession("/CashShopBuy", buyRequest(3, 1, 1, 0, ""), "s"); err != nil {
		t.Fatal(err)
	}
	_, info, _, err := s.Handle("/CashBonusInfo", infoRequest)
	if err != nil {
		t.Fatal(err)
	}
	row, present, _ := wire.Bytes(info, 1)
	count, _, _ := wire.Varint(row, 3)
	if !present || count != 2 {
		t.Fatal("incorrect committed series purchase count", count)
	}
	calls := eco.calls
	code, reply, handled, err := s.HandleSession("/CashBonusReward", claim, "s")
	if err != nil || code != 589 || !handled || eco.calls != calls+1 || len(eco.rewards) != 1 || eco.rewards[0].Count != 2 || len(eco.costs) != 0 {
		t.Fatal("earned bonus not granted through native protocol", code, err, eco.rewards)
	}
	if id, _, _ := wire.Varint(reply, 2); id != 1 {
		t.Fatal("native rewarded ID list missing")
	}
	reopened, err := NewCashBonuses(store, eco, shop, design, packages)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, _, err := reopened.HandleSession("/CashBonusReward", claim, "s")
	if err != nil || !bytes.Equal(reply, replay) || eco.calls != calls+1 {
		t.Fatal("restart replay duplicated bonus", err)
	}
	_, noOp, _, err := reopened.HandleSession("/CashBonusReward", bonusRequest(2, 2, 7, 1), "s")
	bundle, present, _ := wire.Bytes(noOp, 1)
	if err != nil || !present || len(bundle) != 0 || eco.calls != calls+1 {
		t.Fatal("fresh duplicate must return empty grant with claimed IDs", err)
	}
	for _, request := range [][]byte{bonusRequest(1, 2, 7, 2), bonusRequest(4, 1, 7, 1), bonusRequest(5, 2, 7, 99), bonusRequest(6, 2, 7, 2)} {
		if _, _, _, err := reopened.HandleSession("/CashBonusReward", request, "s"); err == nil || eco.calls != calls+1 {
			t.Fatal("conflicting replay, wrong series or unmet bonus accepted")
		}
	}
	_, info, _, err = reopened.Handle("/CashBonusInfo", infoRequest)
	row, _, _ = wire.Bytes(info, 1)
	if id, _, _ := wire.Varint(row, 4); err != nil || id != 1 {
		t.Fatal("claimed IDs missing after restart", err)
	}
}

func TestCashBonusFailedGrantDoesNotMarkClaimed(t *testing.T) {
	shop, eco, store := serviceFixture(t, 0)
	if _, _, _, err := shop.HandleSession("/CashShopBuy", buyRequest(1, 2, 1, 0, ""), "s"); err != nil {
		t.Fatal(err)
	}
	design := &gamedata.CashBonusCatalog{Groups: map[uint64][]gamedata.CashBonusReward{7: {{ID: 1, RequireCount: 1, Reward: gamedata.Reward{Type: 8, ID: 1000, Count: 2}}}}}
	s, err := NewCashBonuses(store, eco, shop, design, []gamedata.CashPackageDesign{{GroupID: 2, ID: 1, PackageType: 8, ContentsGroupID: 7}})
	if err != nil {
		t.Fatal(err)
	}
	eco.fail = true
	request := bonusRequest(1, 2, 7, 1)
	if _, _, _, err := s.HandleSession("/CashBonusReward", request, "s"); err == nil {
		t.Fatal("failed grant accepted")
	}
	v, err := s.load()
	if err != nil || len(v.Claims) != 0 || len(v.Receipts) != 0 {
		t.Fatal("failed grant marked bonus claimed", err)
	}
	eco.fail = false
	if _, _, _, err := s.HandleSession("/CashBonusReward", request, "s"); err != nil {
		t.Fatal("failed grant could not be retried", err)
	}
}
