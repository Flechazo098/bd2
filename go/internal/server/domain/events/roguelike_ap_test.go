package events

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"testing"
	"time"
)

// ScheduleElement.GetCostChargeTime selects daily reset for free RogueLike
// tickets. Existing zero balances mean spent tickets, not an unclaimed grant.
func TestNewRogueTicketAllowanceDoesNotRefillBeforeDailyReset(t *testing.T) {
	store := stateio.NewMemory()
	ctx := command.Context{State: store}
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	e := &Economy{store: stateio.EntrySnapshotStore{Domain: "battle", Bucket: "gameplay"}, initial: map[uint64]uint64{35: 0}}
	wallet, err := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := assets.OpenEquipmentInventory(ctx, stateio.RootStore{})
	if err != nil {
		t.Fatal(err)
	}
	e, err = NewEconomy(ctx, e.store, items, wallet, collection, equipment, resourceCostumes{}, resourceOptions{}, resourceGraph{}, map[uint64]uint64{35: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AttachAPRefresh(map[uint64]uint64{35: 10}, gamedata.HuntingAPDesign{ResetSeconds: 9 * 3600}); err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return now }
	raw, _ := json.Marshal(economySnapshot{Balances: map[uint64]uint64{38: 3}, Receipts: map[string]economyReceipt{}})
	if err := e.store.Save(ctx.State, "event_economy", raw); err != nil {
		t.Fatal(err)
	}
	clock, _ := json.Marshal(economyAPClock{Next: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC).UnixMilli()})
	if err := e.store.Save(ctx.State, "eventapclock", clock); err != nil {
		t.Fatal(err)
	}
	balance, err := e.CurrencyBalance(ctx, 35)
	if err != nil || balance != 10 {
		t.Fatalf("new allowance: %d %v", balance, err)
	}
	if _, err = e.Apply(ctx, "rogue-entry-tickets", []gamedata.Reward{{Type: 35, Count: 10}}, nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reopened := *e
		balance, err = reopened.CurrencyBalance(ctx, 35)
		if err != nil || balance != 0 {
			t.Fatalf("spent tickets refilled before reset: %d %v", balance, err)
		}
	}
	now = now.Add(24 * time.Hour)
	balance, err = e.CurrencyBalance(ctx, 35)
	if err != nil || balance != 10 {
		t.Fatalf("daily reset did not restore allowance: %d %v", balance, err)
	}
	paid, err := e.CurrencyBalance(ctx, 38)
	if err != nil || paid != 3 {
		t.Fatal("free reset changed paid ticket stack")
	}
}
