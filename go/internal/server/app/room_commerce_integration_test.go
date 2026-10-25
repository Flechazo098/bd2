//go:build integration

package app

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

// MyRoomPacket consumes the returned inventory bundle and adds the expanded
// room. LocalText140040 says purchases are delivered to My Room inventory.
func TestNativeRoomPurchasesAndExpansionAreAtomicAndDurable(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	actor := openNicknamePlayer(t, factory, accounts[0])
	tx, err := actor.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixtureCtx := command.Context{State: tx}
	for _, pack := range []int{1, 2} {
		for _, quest := range factory.design.world.Story.Packs[pack].MainQuestIDs {
			if err := actor.assembly.progressState.ClearQuest(fixtureCtx, quest, pack, 0); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
	}
	_, err = actor.assembly.eventEconomy.Apply(command.Context{State: tx}, "room-commerce-fixture", nil, []gamedata.Reward{{Type: 4, Count: 3000000}, {Type: 44, Count: 300}})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	closeNicknamePlayer(t, actor)
	var shared *sharedRuntime
	var registry *playerRegistry
	open := func() {
		var err error
		shared, err = openSharedRuntime(context.Background(), factory.options, factory.design)
		if err != nil {
			t.Fatal(err)
		}
		factory.shared, factory.pvpSource = shared, shared.pvp
		registry = newPlayerRegistry(factory, time.Minute)
	}
	close := func() {
		if registry != nil {
			if err := registry.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			registry = nil
		}
		if shared != nil {
			if err := shared.store.Close(); err != nil {
				t.Fatal(err)
			}
			shared = nil
		}
	}
	t.Cleanup(close)
	open()
	client := friendHTTPLogin(t, registry, accounts[0])
	type balances struct{ gold, coins, furniture, secondFurniture uint64 }
	read := func() balances {
		t.Helper()
		client = friendHTTPLogin(t, registry, accounts[0])
		user := rankingRows(t, client.loginBody, 1)
		if len(user) != 1 {
			t.Fatal("missing own balances")
		}
		gold, _, err := wire.Varint(user[0], 7)
		if err != nil {
			t.Fatal(err)
		}
		coins, _, err := wire.Varint(user[0], 54)
		if err != nil {
			t.Fatal(err)
		}
		body := client.send("/MyRoomUserInfo", wire.AppendVarint(nil, 2, client.owner), 228, 0)
		rooms := rankingRows(t, body, 1)
		if len(rooms) != 1 {
			t.Fatal("missing owned room inventory")
		}
		var furniture, secondFurniture uint64
		for _, row := range rankingRows(t, rooms[0], 5) {
			id, _, err := wire.Varint(row, 2)
			if err != nil {
				t.Fatal(err)
			}
			if id == 101 {
				n, _, err := wire.Varint(row, 4)
				if err != nil {
					t.Fatal(err)
				}
				furniture += n
			}
			if id == 102 {
				n, _, err := wire.Varint(row, 4)
				if err != nil {
					t.Fatal(err)
				}
				secondFurniture += n
			}
		}
		return balances{gold: gold, coins: coins, furniture: furniture, secondFurniture: secondFurniture}
	}
	initial := read()
	payment := func(typ, n uint64) []byte { return wire.AppendVarint(wire.AppendVarint(nil, 3, typ), 4, n) }
	buy := func(id, n, typ, cost uint64) []byte {
		p := wire.AppendVarint(nil, 2, id)
		p = wire.AppendVarint(p, 3, n)
		return wire.AppendBytes(p, 4, payment(typ, cost))
	}
	roomIDs := func() []uint64 {
		t.Helper()
		body := client.send("/MyRoomUserInfo", wire.AppendVarint(nil, 2, client.owner), 228, 0)
		users := rankingRows(t, body, 1)
		if len(users) != 1 {
			t.Fatal("no owned room info")
		}
		var ids []uint64
		for _, row := range rankingRows(t, users[0], 8) {
			id, _, err := wire.Varint(row, 1)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return ids
	}
	initialRooms := roomIDs()
	client.send("/MyRoomExpand", wire.AppendBytes(wire.AppendVarint(nil, 2, 4), 3, payment(44, 200)), 418, 0)
	expectedRooms := append(slices.Clone(initialRooms), 4)
	slices.Sort(expectedRooms)
	if got := roomIDs(); !slices.Equal(got, expectedRooms) {
		t.Fatalf("paid room unavailable immediately: %v want%v", got, expectedRooms)
	}
	client.send("/MyRoomExpand", wire.AppendBytes(wire.AppendVarint(nil, 2, 4), 3, payment(44, 200)), 418, 9003)
	client.send("/MyRoomShopBuy", buy(1, 1, 4, 500000), 246, 0)
	client.seq--
	client.send("/MyRoomShopBuy", buy(1, 1, 4, 500000), 246, 0)
	client.send("/MyRoomShopBuy", buy(1, 1, 44, 50), 246, 0)
	beforeFailure := read()
	if beforeFailure.gold != initial.gold-500000 || beforeFailure.coins != initial.coins-250 || beforeFailure.furniture != initial.furniture+2 {
		t.Fatalf("purchases did not exchange exact currency for furniture: before%+v after%+v", initial, beforeFailure)
	}
	row := func(id, n, typ, cost uint64) []byte {
		p := wire.AppendVarint(nil, 1, id)
		p = wire.AppendVarint(p, 2, n)
		return wire.AppendBytes(p, 3, payment(typ, cost))
	}
	batch := wire.AppendBytes(nil, 2, row(1, 1, 4, 500000))
	batch = wire.AppendBytes(batch, 2, row(2, 2, 44, 100))
	client.send("/MyRoomShopMultiBuy", batch, 646, 9003)
	if got := read(); got != beforeFailure {
		t.Fatalf("insufficient mixed-currency batch changed assets: %+v want%+v", got, beforeFailure)
	}
	info := client.send("/MyRoomShopInfo", nil, 245, 0)
	historyFound := false
	for _, row := range rankingRows(t, info, 1) {
		id, _, _ := wire.Varint(row, 1)
		count, _, _ := wire.Varint(row, 2)
		if id == 1 {
			historyFound = true
			if count != 2 {
				t.Fatalf("failed batch changed purchase count%d", count)
			}
		}
		if id == 2 {
			t.Fatal("failed batch recorded unbought second furniture")
		}
	}
	if !historyFound {
		t.Fatal("failed batch removed previous purchase history")
	}
	client.send("/MyRoomShopBuy", buy(1, 1, 4, 1), 246, 9003)
	if got := read(); got != beforeFailure {
		t.Fatalf("tampered price changed assets: %+v", got)
	}
	bulk := wire.AppendBytes(nil, 2, row(1, 1, 4, 500000))
	bulk = wire.AppendBytes(bulk, 2, row(2, 1, 44, 50))
	client.send("/MyRoomShopMultiBuy", bulk, 646, 0)
	committed := read()
	if committed.gold != beforeFailure.gold-500000 || committed.coins != beforeFailure.coins-50 || committed.furniture != beforeFailure.furniture+1 || committed.secondFurniture != beforeFailure.secondFurniture+1 {
		t.Fatalf("successful bulk purchase changed wrong assets: %+v", committed)
	}
	close()
	open()
	client = friendHTTPLogin(t, registry, accounts[0])
	if got := roomIDs(); !slices.Equal(got, expectedRooms) {
		t.Fatalf("expanded room lost on restart: %v", got)
	}
	info = client.send("/MyRoomShopInfo", nil, 245, 0)
	found := false
	for _, row := range rankingRows(t, info, 1) {
		id, _, _ := wire.Varint(row, 1)
		count, _, _ := wire.Varint(row, 2)
		if id == 1 {
			found = true
			if count != 3 {
				t.Fatalf("purchase count after restart%d", count)
			}
		}
	}
	if !found {
		t.Fatal("purchase history missing after restart")
	}
	if got := read(); got != committed {
		t.Fatalf("purchase assets changed after restart: %+v want%+v", got, committed)
	}
}

// Current MyRoomItemTable 101 has packId=1, questLevel=0; LocalText140059
// requires Knight of Blood Normal. MyRoomShopScrollItem uses that same gate.
func TestNativeRoomShopRequiresCompletedPackBeforeCharging(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.eventEconomy.Apply(ctx, "locked-room-shop-fixture", nil, []gamedata.Reward{{Type: 4, Count: 3000000}, {Type: 44, Count: 300}}); err != nil {
			t.Fatal(err)
		}
	})
	payment := func(typ, id, n uint64) []byte {
		return wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 3, typ), 2, id), 4, n)
	}
	buy := wire.AppendVarint(wire.AppendVarint(nil, 2, 1), 3, 1)
	buy = wire.AppendBytes(buy, 4, payment(4, 0, 500000))
	var beforeItems any
	var beforeGold uint64
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		beforeGold = p.wallet.Snapshot(ctx).Gold
		beforeItems = p.ownedItems.All(ctx)
	})
	if _, err := s.execute("/MyRoomShopBuy", append(appScalar(1, 10), buy...)); err == nil || !strings.Contains(err.Error(), "requires pack 1 difficulty 0") {
		t.Fatalf("locked purchase: %v", err)
	}
	s.reopen(t)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		if p.wallet.Snapshot(ctx).Gold != beforeGold || !reflect.DeepEqual(p.ownedItems.All(ctx), beforeItems) {
			t.Fatal("locked single purchase changed currency or inventory")
		}
	})
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		for _, quest := range factory.design.world.Story.Packs[1].MainQuestIDs {
			if err := p.progressState.ClearQuest(ctx, quest, 1, 0); err != nil {
				t.Fatal(err)
			}
		}
	})
	var lockedID uint64
	for id, product := range factory.design.roomCommerce.Products {
		if item := factory.design.rooms.Items[product.Reward.ID]; item.PackID > 1 && (lockedID == 0 || id < lockedID) {
			lockedID = id
		}
	}
	if lockedID == 0 {
		t.Fatal("GameData fixture lacks another locked pack furnishing")
	}
	product := factory.design.roomCommerce.Products[lockedID]
	price := product.Prices[0]
	first := wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1), 3, payment(4, 0, 500000))
	second := wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(nil, 1, lockedID), 2, 1), 3, payment(price.Type, price.ID, price.Count))
	batch := wire.AppendBytes(wire.AppendBytes(appScalar(1, 11), 2, first), 2, second)
	if _, err := s.execute("/MyRoomShopMultiBuy", batch); err == nil || !strings.Contains(err.Error(), "requires pack") {
		t.Fatalf("mixed unlocked/locked purchase: %v", err)
	}
	s.reopen(t)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		if got := p.wallet.Snapshot(ctx).Gold; got != beforeGold {
			t.Fatalf("locked batch charged gold: %d want %d", got, beforeGold)
		}
		if !reflect.DeepEqual(p.ownedItems.All(ctx), beforeItems) {
			t.Fatal("locked purchase changed inventory")
		}
	})
	if got := s.protocol(t, "/MyRoomShopInfo", appScalar(1, 12)); len(appRows(got, 1)) != 0 {
		t.Fatal("locked purchase recorded a purchase count")
	}
	s.protocol(t, "/MyRoomShopBuy", append(appScalar(1, 13), buy...))
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		if got := p.wallet.Snapshot(ctx).Gold; got != beforeGold-500000 {
			t.Fatalf("completed pack purchase gold=%d want %d", got, beforeGold-500000)
		}
	})
}
