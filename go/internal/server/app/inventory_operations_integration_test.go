//go:build integration

package app

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/runtime/player"
)

func newInventoryGameplay(t *testing.T) *puzzleGameplay {
	t.Helper()
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return g
}

func inventorySetup(t *testing.T, g *puzzleGameplay, fn func(command.Context, *playerAssembly)) {
	t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	fn(command.Context{Identity: command.Identity{AccountID: g.account, SessionID: "setup"}, State: tx, InstanceIDs: g.factory.instanceIDs()}, g.owner.assembly)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func inventoryItem(index, id, kind, count uint64) []byte {
	out := appScalar(1, index)
	out = append(out, appScalar(2, id)...)
	out = append(out, appScalar(3, kind)...)
	return append(out, appScalar(4, count)...)
}
func inventoryEquipment(index, id uint64) []byte {
	return appMessage(2, append(appScalar(1, index), appMessage(5, appScalar(1, id))...))
}
func inventoryOwnedEquipment(t *testing.T, g *puzzleGameplay, index uint64) []byte {
	t.Helper()
	for _, entry := range appRows(g.call("/EquipInfo", nil), 1) {
		if appValue(entry, 1) == index {
			return entry
		}
	}
	return nil
}

func TestInventoryStorageMovesPreserveCountsMetadataAndRollback(t *testing.T) {
	g := newInventoryGameplay(t)
	// CommonPacket.AddKeepItem adds response counts, so movement replies must
	// carry deltas; ResourceTable[1] permits ordinary discard and stacks to9999.
	var index uint64
	baseline := g.itemCount(8, 1)
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		items, err := p.ownedItems.GrantOnce(ctx, "storage-test-resource", []gamedata.BattleReward{{Type: 8, ID: 1, Count: 25}})
		if err != nil {
			t.Fatal(err)
		}
		index = items[0].InvenIndex
	})
	put := func(n uint64) []byte { return appMessage(2, inventoryItem(index, 1, 8, n)) }
	first := g.request("/ItemStoragePut", put(10))
	response := g.execute(first, false)
	stored := appRows(response, 1)
	if len(stored) != 1 || appValue(stored[0], 4) != 10 || appValue(stored[0], 1) == index {
		t.Fatalf("split must create destination stack with delta10: %x", response)
	}
	storageIndex := appValue(stored[0], 1)
	if count := g.itemCount(8, 1); count != baseline+15 {
		t.Fatalf("put backpack count=%d", count)
	}
	if again := g.execute(first, false); !bytes.Equal(again, response) {
		t.Fatal("put retry changed response")
	}
	g.call("/ItemStoragePut", put(5))
	info := g.call("/ItemStorageInfo", nil)
	rows := appRows(info, 1)
	if len(rows) != 1 || appValue(rows[0], 1) != storageIndex || appValue(rows[0], 4) != 15 {
		t.Fatalf("storage did not merge into one stack: %x", info)
	}
	g.reopen()
	info = g.call("/ItemStorageInfo", nil)
	if rows = appRows(info, 1); len(rows) != 1 || appValue(rows[0], 4) != 15 {
		t.Fatal("storage not durable")
	}
	// The first move succeeds inside the batch, then an invalid discard must
	// roll back both sides and discard the assembly's mutated cache.
	failed := g.request("/ItemStorageOut", appMessage(2, inventoryItem(storageIndex, 1, 8, 4)))
	failed.Requests = append(failed.Requests, player.Request{Path: "/ItemDump", Body: append(appScalar(1, 900), appMessage(2, inventoryItem(999999, 1, 8, 1))...)})
	g.execute(failed, true)
	g.reopen()
	if rows = appRows(g.call("/ItemStorageInfo", nil), 1); len(rows) != 1 || appValue(rows[0], 4) != 15 {
		t.Fatal("failed batch changed storage")
	}
	if got := g.itemCount(8, 1); got != baseline+10 {
		t.Fatalf("failed batch changed backpack count: %d", got)
	}
	out := g.request("/ItemStorageOut", appMessage(2, inventoryItem(storageIndex, 1, 8, 7)))
	outReply := g.execute(out, false)
	if deltas := appRows(outReply, 1); len(deltas) != 1 || appValue(deltas[0], 4) != 7 {
		t.Fatal("storage out must return delta")
	}
	// ItemLock flag1 protects discard; a false flag is protobuf's omitted zero.
	target := appValue(appRows(outReply, 1)[0], 1)
	g.call("/ItemLock", appMessage(2, append(appScalar(1, target), appScalar(2, 1)...)))
	g.execute(g.request("/ItemDump", appMessage(2, inventoryItem(target, 1, 8, 1))), true)
	g.call("/ItemLock", appMessage(2, appScalar(1, target)))
	g.call("/ItemDump", appMessage(2, inventoryItem(target, 1, 8, 2)))
	g.reopen()
	if got := g.itemCount(8, 1); got != baseline+15 {
		t.Fatalf("count conservation after put/out/dump: %d want%d", got, baseline+15)
	}
	rows = appRows(g.call("/ItemStorageInfo", nil), 1)
	if len(rows) != 1 || appValue(rows[0], 4) != 8 {
		t.Fatal("storage remaining count mismatch")
	}
	if again := g.execute(out, false); !bytes.Equal(again, outReply) {
		t.Fatal("out retry after reopen changed result")
	}
}

func TestEquipmentStorageAndRankPurchaseUsePersistedGameRules(t *testing.T) {
	g := newInventoryGameplay(t)
	var equipment assets.Equipment
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		var err error
		design, ok := p.design.equipmentCraft.Equipment[43010]
		if !ok {
			t.Fatal("rank fixture needs a craftable UR equipment")
		}
		generated := assets.Equipment{ID: 43010, Level: 9, Rank: []uint64{1, 1, 1}, LockFlag: 1, Mark: "Keep this"}
		options := func(groups []gamedata.OptionGroup) []assets.EquipmentOption {
			var values []assets.EquipmentOption
			for _, group := range groups {
				if len(group.Choices) == 0 {
					t.Fatal("rank fixture option group has no choices")
				}
				values = append(values, assets.EquipmentOption{GroupID: group.ID, ID: group.Choices[0].ID})
			}
			return values
		}
		generated.MainOption = options(design.Main)
		generated.SubOption = options(design.Sub)
		if private := options(design.Private); len(private) != 0 {
			generated.PrivateOption = &private[0]
		}
		equipment, err = p.ownedEquipment.GrantGeneratedOnce(ctx, "rank-test-equipment", generated)
		if err != nil {
			t.Fatal(err)
		}
		if err = p.wallet.ExchangeOnce(ctx, "rank-test-mileage", nil, []gamedata.Reward{{Type: 68, Count: 20000}}); err != nil {
			t.Fatal(err)
		}
	})
	assertMetadata := func(entry []byte, wantRank []uint64) {
		t.Helper()
		base := appRows(entry, 5)
		mark := appRows(entry, 8)
		if len(base) != 1 || len(mark) != 1 || appValue(entry, 1) != equipment.InvenIndex || appValue(entry, 4) != 1 || appValue(base[0], 1) != equipment.ID || appValue(base[0], 2) != equipment.Level || !bytes.Equal(mark[0], []byte(equipment.Mark)) || !slices.Equal(puzzleNumbers(t, base[0], 6), wantRank) {
			t.Fatal("equipment metadata changed while moving or purchasing rank")
		}
		checkOptions := func(field int, expected []assets.EquipmentOption) {
			t.Helper()
			options := appRows(base[0], field)
			if len(options) != len(expected) {
				t.Fatal("equipment options lost")
			}
			for i, option := range options {
				if appValue(option, 1) != expected[i].GroupID || appValue(option, 2) != expected[i].ID {
					t.Fatal("equipment option identity changed")
				}
			}
		}
		checkOptions(3, equipment.MainOption)
		checkOptions(4, equipment.SubOption)
		var private []assets.EquipmentOption
		if equipment.PrivateOption != nil {
			private = append(private, *equipment.PrivateOption)
		} else {
			private = append(private, assets.EquipmentOption{})
		}
		checkOptions(5, private)
	}
	// InventoryPacket performs EquipClear before storing assigned equipment;
	// locked equipment remains storable because storage is not destruction.
	ref := inventoryEquipment(equipment.InvenIndex, equipment.ID)
	put := g.request("/EquipStoragePut", ref)
	putReply := g.execute(put, false)
	if appValue(appRows(putReply, 1)[0], 4) != 1 {
		t.Fatal("storage lost equipment lock")
	}
	if inventoryOwnedEquipment(t, g, equipment.InvenIndex) != nil {
		t.Fatal("stored equipment stayed usable")
	}
	g.reopen()
	stored := appRows(g.call("/EquipStorageInfo", nil), 1)
	if len(stored) != 1 || appValue(stored[0], 1) != equipment.InvenIndex {
		t.Fatal("equipment storage not durable")
	}
	assertMetadata(stored[0], []uint64{1, 1, 1})
	if again := g.execute(put, false); !bytes.Equal(again, putReply) {
		t.Fatal("equipment put retry changed")
	}
	g.call("/EquipStorageOut", ref)
	entry := inventoryOwnedEquipment(t, g, equipment.InvenIndex)
	if entry == nil || appValue(entry, 4) != 1 {
		t.Fatal("retrieval lost lock or instance")
	}
	assertMetadata(entry, []uint64{1, 1, 1})
	// Current GameData: product1600001/4 costs500 type68 and resolves to
	// ticket1 ranks[3,3,4], the21-point ticket described by NameText8065.
	request := append(appScalar(2, equipment.InvenIndex), appScalar(3, 1600001)...)
	request = append(request, appScalar(4, 4)...)
	request = append(request, appMessage(5, inventoryItem(0, 0, 68, 500))...)
	var before uint64
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) { before = p.wallet.Snapshot(ctx).EquipMileage })
	failed := g.request("/EquipRankUpgradeBuy", request)
	failed.Requests = append(failed.Requests, player.Request{Path: "/ItemDump", Body: append(appScalar(1, 901), appMessage(2, inventoryItem(999999, 1, 8, 1))...)})
	g.execute(failed, true)
	g.reopen()
	if ranks := puzzleNumbers(t, appRows(inventoryOwnedEquipment(t, g, equipment.InvenIndex), 5)[0], 6); !slices.Equal(ranks, []uint64{1, 1, 1}) {
		t.Fatalf("failed purchase changed ranks:%v", ranks)
	}
	buy := g.request("/EquipRankUpgradeBuy", request)
	reply := g.execute(buy, false)
	if ranks := puzzleNumbers(t, reply, 1); !slices.Equal(ranks, []uint64{3, 3, 4}) {
		t.Fatalf("rank ticket must replace full vector:%v", ranks)
	}
	assertMetadata(inventoryOwnedEquipment(t, g, equipment.InvenIndex), []uint64{3, 3, 4})
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		if got := p.wallet.Snapshot(ctx).EquipMileage; got != before-500 {
			t.Fatalf("rank debit=%d want%d", got, before-500)
		}
	})
	g.reopen()
	assertMetadata(inventoryOwnedEquipment(t, g, equipment.InvenIndex), []uint64{3, 3, 4})
	if again := g.execute(buy, false); !bytes.Equal(again, reply) {
		t.Fatal("rank retry changed")
	}
	g.execute(g.request("/EquipRankUpgradeBuy", request), true)
	counts := appRows(g.call("/CashShopPurchaseCountInfo", nil), 1)
	found := false
	for _, row := range counts {
		if appValue(row, 1) == 1600001 && appValue(row, 2) == 4 {
			found = true
			if appValue(row, 4) != 1 {
				t.Fatal("rank purchase count not exactly1")
			}
		}
	}
	if !found {
		t.Fatal("rank purchase count missing")
	}
}

func TestEquipmentConversionsCraftOneAndApplyUpgradeAndBreak(t *testing.T) {
	g := newInventoryGameplay(t)
	var char uint64
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.collection.GrantCostumes(ctx, "crafting-character", []uint64{1401}, p.design.regularGacha); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.worldService.CharacterService().EquipmentCharacters() {
			if _, ok := p.design.equipmentCraft.CharTalent[c.ID]; ok {
				char = c.InvenIndex
				break
			}
		}
		if char == 0 {
			t.Fatal("fixture needs crafting character")
		}
		rewards := []gamedata.BattleReward{{Type: 8, ID: 204, Count: 50}, {Type: 8, ID: 201, Count: 50}, {Type: 8, ID: 1, Count: 10000}}
		if _, err := p.ownedItems.GrantOnce(ctx, "conversion-budget", rewards); err != nil {
			t.Fatal(err)
		}
		if err := p.wallet.ExchangeOnce(ctx, "conversion-wallet", nil, []gamedata.Reward{{Type: 4, Count: 1000000}, {Type: 12, Count: 100}}); err != nil {
			t.Fatal(err)
		}
	})
	material := func() []byte {
		t.Helper()
		var out []byte
		inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
			for _, id := range []uint64{204, 201} {
				items, err := p.ownedItems.SelectMutable(ctx, 8, id, 3)
				if err != nil {
					t.Fatal(err)
				}
				for _, i := range items {
					out = append(out, appMessage(4, assets.ItemWire(i))...)
				}
			}
		})
		return out
	}
	makeRequest := func(attempts uint64) []byte {
		out := append(appScalar(2, char), appScalar(3, 1)...)
		out = append(out, material()...)
		out = append(out, appScalar(5, attempts)...)
		out = append(out, appScalar(7, 1)...)
		return append(out, appScalar(8, 3)...)
	}
	before := g.itemCount(8, 204)
	powderBefore := g.itemCount(8, 10)
	var goldBefore uint64
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) { goldBefore = p.wallet.Snapshot(ctx).Gold })
	// A gold budget of1 stops upgrades before any debit; field5 is attempt
	// count. Even0 attempts still makes exactly1 recipe, not0 orN crafts.
	made := g.call("/EquipFromMakingToUpgrade", makeRequest(7))
	equip := appRows(made, 2)
	if len(equip) != 1 || appValue(made, 4) != 0 || appValue(made, 3) != 5 {
		t.Fatalf("craft/upgrade budget result:%x", made)
	}
	index := appValue(equip[0], 1)
	if index == 0 || inventoryOwnedEquipment(t, g, index) == nil {
		t.Fatal("made equipment missing")
	}
	if got := g.itemCount(8, 204); got != before-3 {
		t.Fatalf("field5 incorrectly multiplied craft material:%d", got)
	}
	direct := append(appScalar(2, index), appScalar(3, 1)...)
	direct = append(direct, appScalar(5, 15)...)
	direct = append(direct, appScalar(6, 3)...)
	broke := g.call("/EquipFromUpgradeToBreak", direct)
	if appValue(broke, 3) != 1 || appValue(broke, 6) != 15 || appValue(appRows(appRows(broke, 1)[0], 5)[0], 2) != 1 || len(appRows(broke, 7)) != 1 || inventoryOwnedEquipment(t, g, index) != nil {
		t.Fatalf("upgrade-to-break failed to remove/reward:%x", broke)
	}
	if got := g.itemCount(8, 10); got != powderBefore+4 {
		t.Fatalf("level1 break must grant4 refinement powder: got%d", got)
	}
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		if p.wallet.Snapshot(ctx).Gold != goldBefore-15 {
			t.Fatal("actual upgrade did not charge exactly15 gold")
		}
	})
	combined := g.request("/EquipFromMakingToBreak", makeRequest(0))
	combinedReply := g.execute(combined, false)
	if len(appRows(combinedReply, 2)) != 1 || len(appRows(combinedReply, 8)) != 1 || appValue(combinedReply, 4) != 0 {
		t.Fatalf("make/break zero-attempt contract:%x", combinedReply)
	}
	removed := appValue(appRows(combinedReply, 2)[0], 1)
	if inventoryOwnedEquipment(t, g, removed) != nil {
		t.Fatal("combined break retained equipment")
	}
	if got := g.itemCount(8, 204); got != before-6 {
		t.Fatalf("combined material charge:%d", got)
	}
	g.reopen()
	if got := g.itemCount(8, 10); got != powderBefore+7 {
		t.Fatalf("reopened powder does not include both break rewards: got%d", got)
	}
	if again := g.execute(combined, false); !bytes.Equal(again, combinedReply) {
		t.Fatal("combined retry after reopen changed result")
	}
}
