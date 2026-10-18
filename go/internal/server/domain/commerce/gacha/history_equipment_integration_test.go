//go:build integration

package gacha

import (
	"bytes"
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

func TestEquipmentHistoryPreservesScheduledAndTicketDrawReceipts(t *testing.T) {
	v, e := versionconfig.Find()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := gamedata.CloseDatabaseCache(); e != nil {
			t.Error(e)
		}
	})
	root := v.Resolve("data/resources/GameData")
	catalog, e := gamedata.LoadEquipmentGachaGroups(root, v.GameDataVersion, []uint64{5})
	if e != nil {
		t.Fatal(e)
	}
	design, e := gamedata.LoadCollectionStatistics(root, v.GameDataVersion)
	if e != nil {
		t.Fatal(e)
	}
	// Independently inspected current GameData: scheduled pickup group 5 and
	// ticket-only GachaTable 70100001 belonging to static group 1003.
	group, ok := catalog.Group(5)
	if !ok {
		t.Fatal("missing audited pickup group")
	}
	ticketDraw, ok := catalog.Gacha(70100001)
	if !ok || !ticketDraw.TicketOnly || len(ticketDraw.TicketIDs) == 0 {
		t.Fatal("missing audited exclusive-equipment ticket draw")
	}
	ctx := command.Context{Identity: command.Identity{AccountID: "equipment-history", SessionID: "equipment-session"}, State: stateio.NewMemory()}
	collection, e := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	wallet, e := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{FreeJewelry: 100000})
	if e != nil {
		t.Fatal(e)
	}
	inventory, e := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	stacks, e := gamedata.LoadItemStackDesign(root, v.GameDataVersion)
	if e != nil {
		t.Fatal(e)
	}
	if e = inventory.AttachItemStacks(stacks); e != nil {
		t.Fatal(e)
	}
	tickets, e := inventory.GrantOnce(ctx, "earned-ticket", []gamedata.BattleReward{{ID: ticketDraw.TicketIDs[0], Type: 8, Count: uint64(ticketDraw.Count)}})
	if e != nil {
		t.Fatal(e)
	}
	if len(tickets) != 1 {
		t.Fatal("ticket reward not owned")
	}
	equipment, e := assets.OpenEquipmentInventory(ctx, stateio.RootStore{})
	if e != nil {
		t.Fatal(e)
	}
	fixture, _ := selectionService(t)
	s, e := NewService(nil, fixture.regular, collection, wallet)
	if e != nil {
		t.Fatal(e)
	}
	s.AttachEquipmentGacha(catalog, equipment)
	s.AttachInventory(inventory)
	if e = s.AttachStatistics(design); e != nil {
		t.Fatal(e)
	}
	buy := historyBuy(1, group.OneTimeGachaID)
	_, response, _, e := s.Handle(ctx, "/GachaBuy", buy)
	if e != nil {
		t.Fatal(e)
	}
	bundle, _, e := wire.Bytes(response, 1)
	if e != nil {
		t.Fatal(e)
	}
	awarded := historyFields(t, bundle, 4)
	if len(awarded) != 1 {
		t.Fatalf("scheduled single returned %d actual items", len(awarded))
	}
	material := wire.AppendVarint(nil, 1, tickets[0].InvenIndex)
	material = wire.AppendVarint(material, 2, tickets[0].ID)
	material = wire.AppendVarint(material, 3, tickets[0].Type)
	material = wire.AppendVarint(material, 4, tickets[0].Count)
	request := wire.AppendBytes(historyBuy(2, ticketDraw.ID), 4, material)
	_, ticketResponse, _, e := s.Handle(ctx, "/GachaBuy", request)
	if e != nil {
		t.Fatal(e)
	}
	ticketBundle, _, e := wire.Bytes(ticketResponse, 1)
	if e != nil {
		t.Fatal(e)
	}
	collection, e = roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	s.collection = collection
	_, body, _, e := s.Handle(ctx, "/GachaLog", historyQuery(0))
	if e != nil {
		t.Fatal(e)
	}
	logs := historyFields(t, body, 1)
	if len(logs) != 2 {
		t.Fatalf("two completed equipment draws produced %d history records", len(logs))
	}
	if historyNumber(t, logs[0], 1) != 1003 || historyNumber(t, logs[0], 2) != ticketDraw.ID || historyNumber(t, logs[0], 6) != 2 {
		t.Fatalf("ticket result invented a schedule or lost its static group: %x", logs[0])
	}
	if historyNumber(t, logs[1], 1) != 5 || historyNumber(t, logs[1], 7) != group.PickUpItemID {
		t.Fatal("scheduled pickup metadata changed")
	}
	for i, expected := range [][]byte{ticketBundle, bundle} {
		saved, _, err := wire.Bytes(logs[i], 9)
		if err != nil || !bytes.Equal(saved, expected) {
			t.Fatal("equipment history replaced or lost the actual awarded equipment")
		}
	}
	materials := historyFields(t, logs[0], 12)
	if len(materials) != 1 || !bytes.Equal(materials[0], material) {
		t.Fatal("equipment history lost the actual consumed ticket")
	}
	scheduledCosts := historyFields(t, logs[1], 12)
	scheduledDraw, _ := catalog.Gacha(group.OneTimeGachaID)
	if len(scheduledCosts) != 1 || historyNumber(t, scheduledCosts[0], 1) != 0 || historyNumber(t, scheduledCosts[0], 2) != 0 || historyNumber(t, scheduledCosts[0], 3) != scheduledDraw.PriceType || historyNumber(t, scheduledCosts[0], 4) != scheduledDraw.Price {
		t.Fatal("equipment history lost the diamonds actually spent on the scheduled draw")
	}
	var total uint64
	for field := uint64(6); field <= 14; field++ {
		total += historyTotal(t, body, 2, field) + historyTotal(t, body, 3, field)
	}
	if total != 1+uint64(ticketDraw.Count) {
		t.Fatalf("actual exclusive-equipment reward counts=%d", total)
	}
}
