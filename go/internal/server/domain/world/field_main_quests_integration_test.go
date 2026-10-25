//go:build integration

package world

import (
	"fmt"
	"slices"
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

// Current GameData gives Mirror War six introductory main quests, including
// PvpDeckSave (3) and a practice battle (5); Last Night has three. Both native
// lobby gates consult QuestPacket.IsCompleteQuest before opening.
func TestFieldMainQuestsAdvanceRestoreAndComplete(t *testing.T) {
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	root := versions.Resolve("data/resources/GameData")
	rules, err := gamedata.LoadWorldRules(root, versions.GameDataVersion, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		pack, count, mapID int
		values             [][]uint64
	}{
		{3001, 6, 30011, [][]uint64{{11}, {21}, {1}, {41}, {1}, {61}}},
		{3005, 3, 30051, [][]uint64{{11}, {21, 22}, {31}}},
	} {
		t.Run(fmt.Sprint(tc.pack), func(t *testing.T) {
			field := rules.FieldPacks[tc.pack]
			if len(field.MainQuestIDs) != tc.count || field.IntroQuest != nil {
				t.Fatalf("field main quests: %v, intro=%v", field.MainQuestIDs, field.IntroQuest)
			}
			if _, story := rules.Story.Packs[tc.pack]; story {
				t.Fatal("arena must retain field-card ownership and access rules")
			}
			ctx := command.Context{State: stateio.NewMemory()}
			store := stateio.RootStore{}
			state, err := progress.OpenStore(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			wallet, err := assets.OpenWallet(ctx, store, assets.Currency{})
			if err != nil {
				t.Fatal(err)
			}
			var tickets []assets.Item
			if field.TicketID != 0 {
				tickets = append(tickets, assets.Item{InvenIndex: 1, Type: 19, ID: field.TicketID, Count: 1})
			}
			inventory, err := assets.OpenInventory(ctx, store, tickets)
			if err != nil {
				t.Fatal(err)
			}
			characters, err := roster.OpenCharacterStore(ctx, store, nil, inventory, root, versions.GameDataVersion)
			if err != nil {
				t.Fatal(err)
			}
			collection, err := roster.OpenCollectionStore(ctx, store, nil)
			if err != nil {
				t.Fatal(err)
			}
			equipment, err := assets.OpenEquipmentInventory(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			equipmentDesign, err := gamedata.LoadRewardEquipmentCatalog(root, versions.GameDataVersion)
			if err != nil {
				t.Fatal(err)
			}
			graph, err := gamedata.LoadRewardGraph(root, versions.GameDataVersion)
			if err != nil {
				t.Fatal(err)
			}
			economy, err := events.NewEconomy(ctx, store, inventory, wallet, collection, equipment, rules.QuestCostumes, equipmentDesign, graph, nil)
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{seed: Seed{PackID: 1}, activePack: tc.pack, state: state, storyCatalog: rules.Story, fieldPacks: rules.FieldPacks, packs: rules.Packs, wallet: wallet, inventory: inventory, characters: characters, storyRoster: rules.Characters, questDifficulties: rules.Difficulties, squadLevel: func() (uint64, error) { return 999, nil }}
			source := gamedata.NewSource(root, versions.GameDataVersion)
			service.attachPackDetailDesign(source)
			service.presentationSource = source
			service.AttachFieldPackEconomy(economy)
			var seq uint64
			call := func(path string, quest int, values ...uint64) []byte {
				t.Helper()
				seq++
				req := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, uint64(quest)), 3, uint64(tc.pack))
				for _, v := range values {
					req = wire.AppendVarint(req, 4, v)
				}
				_, body, handled, err := service.Handle(ctx, path, req)
				if err != nil || !handled {
					t.Fatalf("%s quest %d: handled=%t err=%v", path, quest, handled, err)
				}
				return body
			}
			finish := func() {
				t.Helper()
				pending, err := loadStoryCheckpoint(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if pending != nil {
					_, err = service.StoryCheckpoint(ctx, StoryCheckpointRequest{Operation: "complete", StoryCheckpoint: StoryCheckpoint{ID: pending.ID}})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			var gold uint64
			expectedCurrencies := map[int]uint64{}
			for id := 1; id <= tc.count; id++ {
				body, err := service.basePackInfoFor(ctx, tc.pack)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := nestedMessages(body, 2)
				if err != nil || len(rows) != 1 {
					t.Fatalf("pack entry quest rows=%d err=%v", len(rows), err)
				}
				if got, _, _ := wire.Varint(rows[0], 1); got != uint64(id) {
					t.Fatalf("active quest %d, want %d", got, id)
				}
				if service.canClear(ctx, tc.pack, id+1) {
					t.Fatal("later quest may not bypass predecessor")
				}
				design := field.Quests[id]
				if design.AcceptCinemaName != "" {
					cp := StoryCheckpoint{ID: fmt.Sprintf("accept:%d", id), PackID: tc.pack, QuestID: id, Phase: "accept", MapID: tc.mapID, Timeline: design.AcceptCinemaName}
					if _, err := service.StoryCheckpoint(ctx, StoryCheckpointRequest{Operation: "begin", StoryCheckpoint: cp}); err != nil {
						t.Fatal(err)
					}
					finish()
				}
				if tc.pack == 3001 && id == 5 {
					if err := service.ValidateQuestBattle(ctx, tc.pack, []uint64{5}); err != nil {
						t.Fatal(err)
					}
					if err := service.ValidateQuestBattle(ctx, tc.pack, []uint64{6}); err == nil {
						t.Fatal("practice battle must belong to active quest")
					}
					deck, err := gamedata.ResolveQuestBattleDeck(root, versions.GameDataVersion, tc.pack, 1, 1, 0)
					if err != nil || !slices.Contains(deck.QuestIDs, uint64(5)) {
						t.Fatalf("practice battle design: %+v %v", deck, err)
					}
				}
				values := tc.values[id-1]
				call("/QuestUpdate", id, values...)
				service.state, err = progress.OpenStore(ctx, store)
				if err != nil {
					t.Fatal(err)
				}
				wantValues := make([]int, len(values))
				for i, v := range values {
					wantValues[i] = int(v)
				}
				if current, ok := service.state.QuestInPack(id, tc.pack, 0); !ok || !slices.Equal(current.Values, wantValues) {
					t.Fatalf("quest update lost on reconnect: %+v %t", current, ok)
				}
				clear := call("/QuestClear", id)
				next, _, _ := wire.Bytes(clear, 2)
				wantNext := id + 1
				if id == tc.count {
					wantNext = 0
				}
				if got, _, _ := wire.Varint(next, 1); got != uint64(wantNext) {
					t.Fatalf("next quest %d, want %d", got, wantNext)
				}
				pending, err := loadStoryCheckpoint(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if design.CompleteCinemaName != "" && (pending == nil || pending.Timeline != design.CompleteCinemaName || pending.QuestID != id || pending.MapID != tc.mapID) {
					t.Fatalf("missing native completion presentation: %+v", pending)
				}
				finish()
				for _, r := range design.Rewards[0] {
					if r.Type == 4 {
						gold += r.Count
					}
				}
				for _, r := range design.Rewards[0] {
					if field, ok := events.AdditionalCurrencyFields[r.Type]; ok {
						expectedCurrencies[field] += r.Count
					}
				}
				currencies, err := economy.AdditionalCurrencies(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for field, want := range expectedCurrencies {
					if currencies[field] != want {
						t.Fatalf("currency field %d=%d want %d", field, currencies[field], want)
					}
				}
				before := wallet.Snapshot(ctx)
				call("/QuestClear", id)
				currenciesAfter, err := economy.AdditionalCurrencies(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for field, want := range expectedCurrencies {
					if currenciesAfter[field] != want {
						t.Fatal("repeated clear paid arena currency twice")
					}
				}
				if after := wallet.Snapshot(ctx); after != before {
					t.Fatal("repeated clear paid rewards twice")
				}
			}
			if got := wallet.Snapshot(ctx).Gold; got != gold {
				t.Fatalf("gold=%d want %d", got, gold)
			}
			service.state, err = progress.OpenStore(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			body, err := service.basePackInfoFor(ctx, tc.pack)
			if err != nil {
				t.Fatal(err)
			}
			rows, _ := nestedMessages(body, 2)
			if len(rows) != 0 {
				t.Fatal("completed intro restarted on reconnect")
			}
			cleared, err := intsRequest(body, 3)
			if err != nil || len(cleared) != tc.count {
				t.Fatalf("native gate completion list=%v err=%v", cleared, err)
			}
			for id := 1; id <= tc.count; id++ {
				if !slices.Contains(cleared, uint64(id)) {
					t.Fatalf("missing completion %d", id)
				}
			}
		})
	}
}

// Counts and title ranges are fixed from GameData 20261021182514, including
// character-card side quests which are absent from the story-only catalog.
func TestFieldQuestCatalogCoversNativeCardKinds(t *testing.T) {
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	root := versions.Resolve("data/resources/GameData")
	rules, err := gamedata.LoadWorldRules(root, versions.GameDataVersion, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			t.Error(err)
		}
	})
	source := gamedata.NewSource(root, versions.GameDataVersion)
	cases := []struct{ pack, main, side, life int }{
		{1001, 38, 16, 0}, {1002, 36, 12, 0}, {1003, 34, 0, 0}, {1004, 34, 0, 0}, {1005, 29, 0, 0}, {1006, 29, 0, 0}, {1007, 31, 0, 0}, {1008, 33, 0, 0}, {1009, 29, 0, 0}, {1010, 31, 0, 0}, {1011, 30, 0, 0},
		{2001, 18, 0, 0}, {2002, 19, 0, 0}, {2003, 19, 0, 0}, {2004, 21, 0, 0}, {2005, 24, 0, 0}, {2006, 21, 0, 0}, {2007, 23, 0, 0}, {2008, 34, 0, 0}, {2009, 31, 0, 0}, {2010, 26, 0, 0},
		{3001, 6, 0, 0}, {3003, 5, 0, 0}, {3004, 3, 0, 0}, {3005, 3, 0, 0}, {3006, 0, 0, 0}, {3007, 0, 0, 0}, {3008, 0, 0, 0}, {3009, 0, 0, 0}, {3010, 0, 0, 0}, {3011, 0, 0, 15}, {3012, 0, 0, 0},
	}
	if len(rules.FieldPacks) != len(cases) {
		t.Fatalf("field pack count=%d want %d", len(rules.FieldPacks), len(cases))
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.pack), func(t *testing.T) {
			pack, found := rules.FieldPacks[tc.pack]
			if !found || len(pack.MainQuestIDs) != tc.main || len(pack.LifeQuestIDs) != tc.life {
				t.Fatalf("pack %d: main=%v life=%v", tc.pack, pack.MainQuestIDs, pack.LifeQuestIDs)
			}
			side := 0
			for _, q := range pack.Quests {
				if q.Type == 1 {
					side++
				}
			}
			if side != tc.side {
				t.Fatalf("side quests=%d want %d", side, tc.side)
			}
			ctx := command.Context{State: stateio.NewMemory()}
			state, err := progress.OpenStore(ctx, stateio.RootStore{})
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{state: state, storyCatalog: rules.Story, fieldPacks: rules.FieldPacks, packs: rules.Packs, presentationSource: source}
			if tc.main > 0 {
				body, err := service.basePackInfoFor(ctx, tc.pack)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := nestedMessages(body, 2)
				if err != nil || len(rows) != 1 {
					t.Fatalf("entry rows=%d err=%v", len(rows), err)
				}
				if first, _, _ := wire.Varint(rows[0], 1); first != 1 {
					t.Fatalf("first active quest=%d want 1", first)
				}
				if service.packCompleteFor(tc.pack) {
					t.Fatal("new pack reported complete")
				}
				for id := 1; id <= tc.main; id++ {
					if err := state.ClearQuest(ctx, id, tc.pack, 0); err != nil {
						t.Fatal(err)
					}
				}
				card := service.fieldPackDBInfo(pack)
				count, _, _ := wire.Varint(card, 2)
				done, _, _ := wire.Varint(card, 3)
				if count != uint64(tc.main) || done != 1 || !service.packCompleteFor(tc.pack) {
					t.Fatalf("card progress count=%d done=%d", count, done)
				}
			}
			if tc.side > 0 {
				if err := state.AcceptQuest(ctx, 101, tc.pack, 0); err != nil {
					t.Fatal(err)
				}
				active := service.activeSideQuestWires(tc.pack)
				if len(active) != 1 {
					t.Fatalf("active side rows=%d want 1", len(active))
				}
				if id, _, _ := wire.Varint(active[0], 1); id != 101 {
					t.Fatalf("active side=%d want 101", id)
				}
				rows, err := service.questTitleInfo(tc.pack)
				if err != nil || len(rows) != 2 {
					t.Fatalf("character card titles=%d err=%v", len(rows), err)
				}
				request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, uint64(tc.pack))
				_, body, _, err := service.packSubQuestClearInfo(request)
				if err != nil {
					t.Fatal(err)
				}
				rows, err = nestedMessages(body, 1)
				if err != nil || len(rows) != 1 {
					t.Fatalf("side preview=%d %v", len(rows), err)
				}
				if count, _, _ := wire.Varint(rows[0], 2); count != 0 {
					t.Fatalf("uncompleted side chain count=%d", count)
				}
				for id := 101; id <= 100+tc.side; id++ {
					if err := state.ClearQuest(ctx, id, tc.pack, 0); err != nil {
						t.Fatal(err)
					}
				}
				_, body, _, err = service.packSubQuestClearInfo(request)
				if err != nil {
					t.Fatal(err)
				}
				rows, err = nestedMessages(body, 1)
				if err != nil || len(rows) != 1 {
					t.Fatalf("completed side preview=%d %v", len(rows), err)
				}
				if count, _, _ := wire.Varint(rows[0], 2); count != 1 {
					t.Fatalf("completed side chapter count=%d want 1", count)
				}
			}
			if pack.IntroQuest != nil && (tc.pack != 3006 || service.firstUnclearedQuestFor(tc.pack) != 0) {
				t.Fatal("local watched introduction became a claimable quest")
			}
		})
	}
}
