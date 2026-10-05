package commerce

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

type clearInventory struct{ items []player.Item }

func (i *clearInventory) All() []player.Item { return i.items }
func clearRequest(kind, ticket uint64) []byte {
	b := wire.AppendVarint(nil, 1, 1)
	b = wire.AppendVarint(b, 2, kind)
	row := wire.AppendVarint(nil, 1, 10)
	row = wire.AppendVarint(row, 2, ticket)
	row = wire.AppendVarint(row, 3, 2)
	row = wire.AppendVarint(row, 4, 0)
	return wire.AppendBytes(b, int(kind)+3, row)
}
func TestClearPackageRequiresServerProgressAndPremiumEntitlement(t *testing.T) {
	store := stateio.NewMemory()
	eco := &purchaseEconomy{}
	items := &clearInventory{}
	design := &gamedata.ClearPackageCatalog{Rewards: []gamedata.ClearPackageRewardDesign{{Kind: 0, GroupID: 10, TicketID: 12, TargetID: 2, RandomBoxID: 100, Type: 0}, {Kind: 0, GroupID: 10, TicketID: 77, TargetID: 2, RandomBoxID: 101, Type: 1}, {Kind: 1, GroupID: 10, TicketID: 12, TargetID: 2, RandomBoxID: 102, Type: 0}}}
	s, err := NewClearPackages(store, design, eco, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/ClearPackageReward", clearRequest(0, 12)); err == nil || eco.calls != 0 {
		t.Fatal("client spoofed clear progress")
	}
	s.AttachProgress(func(pack, level uint64) bool { return pack == 2 && level == 0 }, nil)
	if _, _, _, err = s.Handle("/ClearPackageReward", clearRequest(0, 77)); err == nil {
		t.Fatal("unpaid premium claim accepted")
	}
	code, _, ok, err := s.Handle("/ClearPackageReward", clearRequest(0, 12))
	if err != nil || !ok || code != 286 || eco.rewards[0].ID != 100 {
		t.Fatal(code, err)
	}
	items.items = []player.Item{{Type: 19, ID: 77, Count: 1}}
	if _, _, _, err = s.Handle("/ClearPackageReward", clearRequest(0, 77)); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewClearPackages(store, design, eco, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = reloaded.Handle("/ClearPackageReward", clearRequest(0, 77)); err != nil || eco.calls != 2 {
		t.Fatal("restart duplicated grant", err, eco.calls)
	}
	p, e, err := reloaded.RewardDBInfos()
	if err != nil || len(p) != 2 || len(e) != 0 {
		t.Fatal(p, e, err)
	}
	if _, _, _, err = reloaded.Handle("/ClearPackageReward", clearRequest(1, 12)); err == nil {
		t.Fatal("unimplemented evil progress granted reward")
	}
}
