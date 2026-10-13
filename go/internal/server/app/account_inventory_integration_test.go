//go:build integration

package app

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestUnlimitedInventoryIsAccountScopedAndPreservesPurchases(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	open := func(account string) *puzzleGameplay {
		t.Helper()
		instance, err := factory.open(account)
		if err != nil {
			t.Fatal(err)
		}
		g := &puzzleGameplay{t: t, factory: factory, account: account, owner: instance}
		t.Cleanup(func() {
			if g.owner != nil {
				if err := g.owner.Close(); err != nil {
					t.Error(err)
				}
			}
		})
		return g
	}
	a, b := open(accounts[0]), open(accounts[1])
	// UserDBInfo capacity fields 5/6/10/15 are defined in the 2.36.7 client;
	// limits and prices come from the current repository GameDefaultTable.
	login := func(g *puzzleGameplay) assets.InventorySlotCounts {
		t.Helper()
		user := profileBytes(t, g.call("/LoginUser", nil), 1)
		return assets.InventorySlotCounts{
			Items: profileVarint(t, user, 5), Storage: profileVarint(t, user, 6),
			Equipment: profileVarint(t, user, 10), EquipmentStorage: profileVarint(t, user, 15),
		}
	}
	aBaseline, bBaseline := login(a), login(b)
	design := factory.design.slotDesign
	if aBaseline.Items+1 >= design.Items.Maximum || aBaseline.Equipment+1 >= design.Equipment.Maximum ||
		bBaseline.Items >= design.Items.Maximum || bBaseline.Equipment >= design.Equipment.Maximum {
		t.Fatal("test seed must distinguish purchased and advertised maximum capacity")
	}
	a.call("/InvenAddSlot", wire.AppendVarint(nil, 2, 1))
	a.call("/EquipAddSlot", wire.AppendVarint(nil, 2, 1))
	purchased := aBaseline
	purchased.Items++
	purchased.Equipment++
	if got := login(a); got != purchased {
		t.Fatalf("purchase login = %+v, want %+v", got, purchased)
	}
	writeSettings := func(unlimited bool) {
		t.Helper()
		document := map[string]any{"version": 2, "accounts": map[string]any{
			a.account: map[string]any{"inventory": map[string]bool{"unlimited": unlimited}},
		}}
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(factory.options.devToolsConfig, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSettings(true)
	maximum := purchased
	maximum.Items, maximum.Equipment = design.Items.Maximum, design.Equipment.Maximum
	if got := login(a); got != maximum {
		t.Fatalf("enabled A login = %+v, want %+v", got, maximum)
	}
	if got := login(b); got != bBaseline {
		t.Fatalf("A override changed B login: %+v, want %+v", got, bBaseline)
	}
	a.reopen()
	b.reopen()
	if got := login(a); got != maximum {
		t.Fatalf("reloaded A login = %+v, want %+v", got, maximum)
	}
	if got := login(b); got != bBaseline {
		t.Fatalf("reloaded B login = %+v, want %+v", got, bBaseline)
	}
	assertPurchasedState := func() {
		t.Helper()
		tx, err := a.owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		data, found, err := a.owner.assembly.scope.LoadEntry(tx, "items", "slots", "capacity")
		if err != nil || !found {
			t.Fatalf("durable purchased capacity missing: %v", err)
		}
		var saved assets.InventorySlotCounts
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if saved != purchased {
			t.Fatalf("advertised override changed durable purchases: %+v, want %+v", saved, purchased)
		}
		if got, err := a.owner.assembly.inventorySlots.InventorySlotCounts(command.Context{Identity: command.Identity{AccountID: a.account}}); err != nil || got != maximum {
			t.Fatalf("account context did not select override: %+v %v", got, err)
		}
	}
	assertPurchasedState()
	writeSettings(false)
	if got := login(a); got != purchased {
		t.Fatalf("disabled A login lost purchased capacity: %+v, want %+v", got, purchased)
	}
	if got := login(b); got != bBaseline {
		t.Fatalf("disabled A switch changed B: %+v, want %+v", got, bBaseline)
	}
	a.reopen()
	if got := login(a); got != purchased {
		t.Fatalf("reloaded disabled A login = %+v, want %+v", got, purchased)
	}
}
