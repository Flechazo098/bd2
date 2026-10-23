//go:build integration

package world

import (
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type fieldPackLedger struct{ grants []string }

func (l *fieldPackLedger) Apply(_ command.Context, identity string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error) {
	l.grants = append(l.grants, identity)
	return nil, nil
}

func openCombatPackService(t *testing.T) (*Service, command.Context, *fieldPackLedger) {
	t.Helper()
	ctx := command.Context{State: stateio.NewMemory()}
	collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tickets []assets.Item
	for i, id := range []uint64{13001, 13003, 13002, 14, 17, 3007} {
		tickets = append(tickets, assets.Item{InvenIndex: uint64(i + 1), Type: 19, ID: id, Count: 1})
	}
	inventory, err := assets.OpenInventory(ctx, stateio.RootStore{}, tickets)
	if err != nil {
		t.Fatal(err)
	}
	ledger := &fieldPackLedger{}
	service := &Service{
		state:            progress.NewStore(),
		collection:       collection,
		inventory:        inventory,
		fieldPackEconomy: ledger,
		storyCatalog:     &gamedata.StoryCatalog{Packs: map[int]gamedata.StoryPack{}},
		fieldPacks: map[int]gamedata.FieldPack{
			3001: {ID: 3001, Type: 3, HasOpenRule: true, TicketID: 13001, BuyRewards: []gamedata.Reward{{Type: 4, Count: 10}}},
			3003: {ID: 3003, Type: 4, HasOpenRule: true, TicketID: 13003, BuyRewards: []gamedata.Reward{{Type: 18, Count: 5}}},
			3005: {ID: 3005, Type: 7, HasOpenRule: true, TicketID: 14},
			3008: {ID: 3008, Type: 10, HasOpenRule: true, TicketID: 17},
			3007: {ID: 3007, Type: 9, HasOpenRule: true, TicketID: 3007},
			3002: {ID: 3002, Type: 3, HasOpenRule: true, TicketID: 13002, BuyPrice: 100},
		},
	}
	return service, ctx, ledger
}

func packRowsBought(t *testing.T, service *Service, ctx command.Context) map[uint64]bool {
	t.Helper()
	rows, err := service.storyPackDBInfoRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[uint64]bool, len(rows))
	for _, row := range rows {
		id, _, _ := wire.Varint(row, 1)
		bought, _, _ := wire.Varint(row, 8)
		out[id] = bought == 1
	}
	return out
}

// A free combat pack becomes owned once its ContentOpen ticket exists. The
// client only lists purchased packs, so the login claim is the only way the
// pack card and its advertised buy rewards can appear.
func TestClaimOpenFieldPacksOwnsUnlockedCombatPacks(t *testing.T) {
	service, ctx, ledger := openCombatPackService(t)
	if err := service.ClaimOpenFieldPacks(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{3001, 3003, 3005, 3008} {
		if _, owned := service.collection.Grant(packPurchaseIdentity(id)); !owned {
			t.Fatalf("pack %d was not claimed", id)
		}
	}
	for _, id := range []int{3002, 3007} {
		if _, owned := service.collection.Grant(packPurchaseIdentity(id)); owned {
			t.Fatalf("pack %d must not be claimed", id)
		}
	}
	if len(ledger.grants) != 4 {
		t.Fatalf("claimed packs = %v, want the four combat packs", ledger.grants)
	}
	if err := service.ClaimOpenFieldPacks(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ledger.grants) != 4 {
		t.Fatalf("second login claimed again: %v", ledger.grants)
	}
	rows := packRowsBought(t, service, ctx)
	for _, id := range []uint64{3001, 3003, 3005, 3008} {
		if !rows[id] {
			t.Fatalf("pack %d missing purchased row: %v", id, rows)
		}
	}
	for _, id := range []uint64{3002, 3007} {
		if _, listed := rows[id]; listed {
			t.Fatalf("pack %d must stay unlisted: %v", id, rows)
		}
	}
}

// The client can also ask to buy an unlocked combat pack directly; the same
// purchase path must record it and its rewards.
func TestPackBuyClaimsUnlockedCombatFieldPack(t *testing.T) {
	service, ctx, ledger := openCombatPackService(t)
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 3001)
	code, _, handled, err := service.Handle(ctx, "/PackBuy", request)
	if err != nil || !handled {
		t.Fatalf("PackBuy: handled=%v err=%v", handled, err)
	}
	if code != 6 {
		t.Fatalf("PackBuy code = %d, want 6", code)
	}
	if _, owned := service.collection.Grant(packPurchaseIdentity(3001)); !owned {
		t.Fatal("PackBuy did not record the purchase")
	}
	if len(ledger.grants) != 1 || ledger.grants[0] != packPurchaseIdentity(3001) {
		t.Fatalf("PackBuy rewards = %v", ledger.grants)
	}
	if _, _, _, err := service.Handle(ctx, "/PackBuy", request); err != nil {
		t.Fatal(err)
	}
	if len(ledger.grants) != 1 {
		t.Fatalf("repeated PackBuy granted again: %v", ledger.grants)
	}
}

// Character, event, square and life cards (Define_PackType 1, 6, 11, 14) open
// with the ticket granted by clearing Edge of Dimensions; unscheduled ones are
// claimed like the combat packs, a scheduled event card is not.
func TestClaimOpenFieldPacksOwnsTicketGatedContentCards(t *testing.T) {
	service, ctx, ledger := openCombatPackService(t)
	inventory, err := assets.OpenInventory(ctx, stateio.RootStore{}, []assets.Item{{InvenIndex: 1, Type: 19, ID: 11, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	service.inventory = inventory
	service.fieldPacks = map[int]gamedata.FieldPack{
		1001: {ID: 1001, Type: 1, HasOpenRule: true, TicketID: 11},
		2001: {ID: 2001, Type: 6, HasOpenRule: true, TicketID: 11},
		2004: {ID: 2004, Type: 6, HasOpenRule: true, TicketID: 11, UseSchedule: 1},
		3009: {ID: 3009, Type: 11, HasOpenRule: true, TicketID: 11},
		3012: {ID: 3012, Type: 14, HasOpenRule: true, TicketID: 11},
		3013: {ID: 3013, Type: 14, HasOpenRule: true, TicketID: 99},
	}
	if err := service.ClaimOpenFieldPacks(ctx); err != nil {
		t.Fatal(err)
	}
	rows := packRowsBought(t, service, ctx)
	for _, id := range []uint64{1001, 2001, 3009, 3012} {
		if !rows[id] {
			t.Fatalf("content card %d missing purchased row: %v", id, rows)
		}
	}
	for _, id := range []uint64{2004, 3013} {
		if _, listed := rows[id]; listed {
			t.Fatalf("pack %d must stay unlisted: %v", id, rows)
		}
	}
	if len(ledger.grants) != 4 {
		t.Fatalf("claimed = %v, want four cards", ledger.grants)
	}
	if err := service.ClaimOpenFieldPacks(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ledger.grants) != 4 {
		t.Fatalf("second login claimed again: %v", ledger.grants)
	}
}
