//go:build integration

package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

// ResourceTable 11 (1★ star-up material) is an ordinary stackable resource.
// An account's override merges its stacks and lifts the limit for that account
// alone; once it is off, the stacks split back to the limit. Both happen on
// the next request, whose reply asks the plugin to reload the inventory.
func TestUnlimitedItemStacksMergeAndSplitBackForTheirAccountOnly(t *testing.T) {
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
	const itemType, itemID = 8, 11
	limit := factory.design.itemStacks.Limits[[2]uint64{itemType, itemID}]
	if limit == 0 {
		t.Fatal("GameData has no stack limit for the star-up material")
	}
	writeSettings := func(unlimited bool) {
		t.Helper()
		accounts := map[string]any{}
		if unlimited {
			accounts[a.account] = map[string]any{"items": map[string]bool{"unlimited_stacks": true}}
		}
		data, err := json.Marshal(map[string]any{"version": 3, "accounts": accounts})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(factory.options.devToolsConfig, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(g *puzzleGameplay, identity string, count uint64) {
		t.Helper()
		tx, err := g.owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		ctx := command.Context{Identity: command.Identity{AccountID: g.account}, State: tx, InstanceIDs: factory.instanceIDs()}
		if _, err := g.owner.assembly.ownedItems.GrantOnce(ctx, identity, []gamedata.BattleReward{{Type: itemType, ID: itemID, Count: count}}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	stacks := func(g *puzzleGameplay) (total, largest uint64, count int) {
		t.Helper()
		tx, err := g.owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		for _, item := range g.owner.assembly.ownedItems.All(command.Context{State: tx}) {
			if item.Type == itemType && item.ID == itemID {
				total += item.Count
				largest = max(largest, item.Count)
				count++
			}
		}
		return total, largest, count
	}

	// Ordinary limits scatter a large grant over several stacks.
	grant(a, "scatter-a", 2*limit+1)
	scattered, _, pieces := stacks(a)
	if pieces < 3 {
		t.Fatalf("limited grant made %d stacks, want at least 3", pieces)
	}
	bBefore, _, _ := stacks(b)

	// refreshed runs one ordinary request and reports whether its reply asks
	// the LocalIdentity plugin to reload the inventory.
	refreshed := func(g *puzzleGameplay) bool {
		t.Helper()
		reply, err := g.owner.Execute(context.Background(), g.request("/ItemInfo", nil))
		if err != nil || len(reply.Responses) != 1 {
			t.Fatalf("/ItemInfo: responses=%d error=%v", len(reply.Responses), err)
		}
		flag, present, err := wire.Varint(reply.Responses[0].Notification, ItemStackRefreshNotifyField)
		return err == nil && present && flag == itemStackRefreshItems
	}

	// Switching on merges them into one stack on the very next request, without a new login.
	writeSettings(true)
	if !refreshed(a) {
		t.Fatal("enabling did not ask the client to reload its inventory")
	}
	if total, _, count := stacks(a); total != scattered || count != 1 {
		t.Fatalf("enabled A: total=%d stacks=%d, want %d in one stack", total, count, scattered)
	}
	if refreshed(a) || refreshed(b) {
		t.Fatal("an unchanged inventory asked for another reload")
	}
	grant(a, "stack-a", limit)
	grant(b, "stack-b", 2*limit)
	merged, _, count := stacks(a)
	if merged != scattered+limit || count != 1 {
		t.Fatalf("unlimited A grant: total=%d stacks=%d, want %d in one stack", merged, count, scattered+limit)
	}
	if total, largest, _ := stacks(b); total != bBefore+2*limit || largest > limit {
		t.Fatalf("A's override changed B: total=%d largest=%d limit=%d", total, largest, limit)
	}
	a.reopen()
	if total, _, count := stacks(a); total != merged || count != 1 {
		t.Fatalf("merge did not persist: total=%d stacks=%d", total, count)
	}

	// Switching off splits the stack back to the limit on the next request;
	// nothing is lost while slots remain.
	writeSettings(false)
	if !refreshed(a) {
		t.Fatal("disabling did not ask the client to reload its inventory")
	}
	total, largest, count := stacks(a)
	if total != merged || largest > limit || uint64(count) != (merged+limit-1)/limit {
		t.Fatalf("disabled A: total=%d largest=%d stacks=%d, want %d split by %d", total, largest, count, merged, limit)
	}
	a.reopen()
	if again, _, _ := stacks(a); again != total {
		t.Fatalf("split did not persist: %d, want %d", again, total)
	}
	grant(a, "stack-a-limited", limit+1)
	if _, largest, _ := stacks(a); largest > limit {
		t.Fatalf("limited A grant exceeded the stack limit: %d > %d", largest, limit)
	}
}
