//go:build integration

package world

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

func eventMonsterIntegration(t *testing.T, pack int) (command.Context, *Service, *assets.Inventory, func(int)) {
	t.Helper()
	ctx, service, source := fieldQuestIntegration(t, pack)
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	root := versions.Resolve("data/resources/GameData")
	service.attachFieldMonsterDesign(source)
	service.attachPackDetailDesign(source)
	service.fieldReset, err = gamedata.LoadFieldResetSchedule(root, versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	service.monsterNow = func() time.Time { return time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC) }
	if err := service.AttachFieldMonsterState(stateio.RootStore{}); err != nil {
		t.Fatal(err)
	}
	items, err := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := gamedata.LoadItemStackDesign(root, versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := items.AttachItemStacks(stacks); err != nil {
		t.Fatal(err)
	}
	service.characters, err = roster.OpenCharacterStore(ctx, stateio.RootStore{}, []roster.Character{{InvenIndex: 1, ID: 1, HP: 1000, Level: 1}}, items, root, versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	service.decks, err = deck.OpenStore(ctx, stateio.RootStore{}, deck.Seed{Version: versions.GameVersion, FieldCharControlDeckType: 1, FieldDeck: []deck.FieldEntry{{CharacterInvenIndex: 1, Slot: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	buffs, err := gamedata.LoadFieldBuffDesign(root, versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachFieldBuffRuntime(buffs); err != nil {
		t.Fatal(err)
	}
	setMap := func(mapID int) {
		t.Helper()
		request := wire.AppendBytes(wire.AppendVarint(nil, 2, uint64(pack)), 3, []byte(fmt.Sprintf(`{"MapId":%d}`, mapID)))
		if err := service.state.SaveUserPosition(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, service, items, setMap
}

func fieldMonsterEventRequest(seq, id, dash uint64) []byte {
	return wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, id), 3, dash)
}

// DisableMonster sends FieldMonsterEvent(false) for an inactive ordinary
// monster; isRefresh is only local and absent from the protobuf request.
func TestOrdinaryMonsterRefreshDoesNotBecomeVictory(t *testing.T) {
	ctx, service, items, setMap := eventMonsterIntegration(t, 2)
	setMap(22)
	request := fieldMonsterEventRequest(1, 101, 0)
	_, response, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", request)
	if err != nil {
		t.Fatalf("inactive ordinary refresh must succeed before the new chapter's main quest is complete: %v", err)
	}
	if len(response) != 0 {
		t.Fatal("inactive ordinary acknowledgement must not re-enter the client's RefreshRegen/DisableMonster loop")
	}
	// The current client re-enters RefreshRegen only for a non-null MonsterInfo
	// with MonsterId > 0. All five real forest cleanup senders must terminate.
	for i, id := range []uint64{101, 102, 103, 104, 105} {
		cleanupRequest := fieldMonsterEventRequest(uint64(100+i), id, 0)
		_, acknowledgement, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", cleanupRequest)
		if err != nil || len(acknowledgement) != 0 {
			t.Fatalf("forest cleanup %d would re-enter the client monster-refresh callback: %v", id, err)
		}
		_, repeated, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", cleanupRequest)
		if err != nil || len(repeated) != 0 {
			t.Fatalf("forest cleanup %d retry would restore a looping reply: %v", id, err)
		}
	}
	state, err := service.loadMonsterState(ctx)
	if err != nil || len(state.Monsters) != 0 || len(state.Claims) != 0 || len(items.All(ctx)) != 0 {
		t.Fatalf("inactive refresh invented a spawn, claim or reward: %+v %v", state, err)
	}
	if err := service.state.ClearQuest(ctx, 43, 2, 0); err != nil {
		t.Fatal(err)
	}
	_, response, _, err = service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", fieldMonsterEventRequest(2, 101, 0))
	if err != nil {
		t.Fatalf("ordinary old-map cleanup must not need current collision-map authorization: %v", err)
	}
	if len(response) != 0 {
		t.Fatal("ordinary cleanup acknowledgement must not refresh or count a battle monster as a reward")
	}
	state, err = service.loadMonsterState(ctx)
	if err != nil || state.Monsters[monsterKey(2, 101)].Defeated || len(state.Claims) != 0 {
		t.Fatalf("ordinary synchronization became a battle victory: %+v %v", state, err)
	}
	count, err := service.packMonsterDefeatedCount(ctx, 2)
	if err != nil || count != 0 {
		t.Fatalf("ordinary sync must not add a forest kill: %d %v", count, err)
	}

}

// Pack 2's private crash monster 4011 uses FieldBuff203 (50 fixed damage)
// and CrashAvailableFromDash. It grants no battle reward on collision.
func TestPrivateMonsterCollisionDashDamageAndRefreshRetries(t *testing.T) {
	for _, dash := range []uint64{0, 1} {
		t.Run(fmt.Sprintf("dash%d", dash), func(t *testing.T) {
			ctx, service, items, setMap := eventMonsterIntegration(t, 2)
			now := service.monsterTime()
			service.monsterNow = func() time.Time { return now }
			placements, err := service.monsterMaps(2)
			if err != nil {
				t.Fatal(err)
			}
			setMap(21)
			request := fieldMonsterEventRequest(1, 4011, dash)
			if _, _, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", request); err == nil {
				t.Fatalf("active collision from another map must retain map authorization: %v", err)
			}
			if hp, err := service.characters.CurrentHealth(ctx, 1); err != nil || hp != 1000 || len(items.All(ctx)) != 0 {
				t.Fatalf("rejected remote collision changed health or rewards: hp=%d err=%v", hp, err)
			}
			setMap(placements[4011][0])
			_, response, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", request)
			if err != nil {
				t.Fatal(err)
			}
			wantHP := uint64(950)
			if dash == 1 {
				wantHP = 1000
			}
			hp, err := service.characters.CurrentHealth(ctx, 1)
			if err != nil || hp != wantHP || len(items.All(ctx)) != 0 {
				t.Fatalf("private collision health=%d want=%d, no rewards permitted: %v", hp, wantHP, err)
			}
			setMap(21)
			_, retry, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", request)
			if err != nil || !bytes.Equal(response, retry) {
				t.Fatalf("settled request retry must work after leaving the collision map: %v", err)
			}
			if _, cleanup, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", fieldMonsterEventRequest(2, 4011, 0)); err != nil || len(cleanup) != 0 {
				t.Fatalf("inactive cleanup after a collision must synchronize without damaging again: %v", err)
			}
			hp, _ = service.characters.CurrentHealth(ctx, 1)
			if hp != wantHP {
				t.Fatal("collision retry or inactive refresh applied damage twice")
			}
			if _, _, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", fieldMonsterEventRequest(1, 4011, 1-dash)); err == nil {
				t.Fatalf("changing a settled request must fail explicitly: %v", err)
			}
			setMap(placements[4011][0])
			now = now.Add(599 * time.Second)
			if _, _, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", fieldMonsterEventRequest(3, 4011, 0)); err != nil {
				t.Fatal(err)
			}
			if hp, err := service.characters.CurrentHealth(ctx, 1); err != nil || hp != wantHP {
				t.Fatalf("cooling monster damaged the player before it respawned: hp=%d err=%v", hp, err)
			}
			now = now.Add(time.Second)
			if _, _, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", fieldMonsterEventRequest(4, 4011, 0)); err != nil {
				t.Fatal(err)
			}
			if hp, err := service.characters.CurrentHealth(ctx, 1); err != nil || hp != wantHP-50 {
				t.Fatalf("respawned monster did not apply the next real collision: hp=%d err=%v", hp, err)
			}
		})
	}
}

func TestRewardMonsterCollisionGrantsOnceAndInactiveRefreshIsSafe(t *testing.T) {
	ctx, service, items, setMap := eventMonsterIntegration(t, 2)
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	graph, err := gamedata.LoadRewardGraph(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	graph.SetSampler(func(uint64) (uint64, error) { return 0, nil })
	wallet, err := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service.researchEconomy = eventInventoryRewards{items: items, wallet: wallet, graph: graph}
	placements, err := service.monsterMaps(2)
	if err != nil {
		t.Fatal(err)
	}
	setMap(placements[4001][0])
	request := fieldMonsterEventRequest(1, 4001, 1)
	_, response, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", request)
	if err != nil {
		t.Fatal(err)
	}
	awarded := items.All(ctx)
	if wallet.Snapshot(ctx).Gold != 4200 || len(awarded) != 0 {
		t.Fatal("reward collision did not open its gold box into the wallet")
	}
	_, retry, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", request)
	if err != nil || !bytes.Equal(response, retry) || !slices.Equal(awarded, items.All(ctx)) || wallet.Snapshot(ctx).Gold != 4200 {
		t.Fatalf("reward collision retry must preserve its reply: %v", err)
	}
	setMap(21)
	if _, cleanup, _, err := service.handleFieldMonsterEvent(ctx, "/FieldMonsterEvent", fieldMonsterEventRequest(2, 4001, 0)); err != nil || len(cleanup) != 0 || !slices.Equal(awarded, items.All(ctx)) || wallet.Snapshot(ctx).Gold != 4200 {
		t.Fatalf("inactive reward refresh must neither fail nor grant another item: %v", err)
	}
}

type eventInventoryRewards struct {
	items  *assets.Inventory
	wallet *assets.Wallet
	graph  *gamedata.RewardGraph
}

func (e eventInventoryRewards) Apply(ctx command.Context, identity string, _ []gamedata.Reward, rewards []gamedata.Reward) ([]byte, error) {
	definitions := make([]gamedata.BattleReward, len(rewards))
	for i, reward := range rewards {
		definitions[i] = gamedata.BattleReward(reward)
	}
	resolved, err := e.graph.Resolve(definitions)
	if err != nil {
		return nil, err
	}
	var gold []gamedata.Reward
	var owned []gamedata.BattleReward
	var bundle []byte
	for _, reward := range resolved {
		if reward.Type == 4 {
			gold = append(gold, gamedata.Reward(reward))
			bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(assets.Item{Type: 4, Count: reward.Count}))
		} else {
			owned = append(owned, reward)
		}
	}
	if _, err := e.wallet.GrantQuestOnce(ctx, identity, gold); err != nil {
		return nil, err
	}
	items, err := e.items.GrantOnce(ctx, identity, owned)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
	}
	return bundle, nil
}
