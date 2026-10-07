//go:build integration

package world

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/wire"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/stateio"
)

// Quest_NPC_01 takes one NPC acceptance and continues through forest, cave,
// and final map travel. Completed stages must survive pack entry: NPCInfo and
// the HUD offer missing completed IDs again after the active chain ends.
func TestForestSideQuestCompletionSurvivesPackEntry(t *testing.T) {
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := gamedata.LoadItemStackDesign(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			t.Error(err)
		}
	})
	catalog, err := gamedata.LoadStoryCatalog(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	questCostumes, err := gamedata.LoadQuestCostumes(versions.Resolve("data/resources/GameData"), versions.GameDataVersion,
		map[int]map[int]gamedata.QuestDesign{1: catalog.Packs[1].Quests}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := accountstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	})
	run := func(action func(command.Context, *Service)) {
		t.Helper()
		tx, err := repo.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		ctx := command.Context{Identity: command.Identity{AccountID: "forest-side-quest", SessionID: "authenticated"}, State: tx}
		state, err := progress.OpenStore(ctx, stateio.RootStore{})
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{})
		if err != nil {
			t.Fatal(err)
		}
		inventory, err := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := inventory.AttachItemStacks(stacks); err != nil {
			t.Fatal(err)
		}
		collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		service := &Service{seed: Seed{PackID: 1}, activePack: 1, state: state, wallet: wallet, inventory: inventory,
			storyCatalog: catalog, collection: collection, questCostumes: questCostumes}
		service.attachFieldMonsterDesign(gamedata.NewSource(versions.Resolve("data/resources/GameData"), versions.GameDataVersion))
		if err := service.AttachFieldMonsterState(stateio.RootStore{}); err != nil {
			t.Fatal(err)
		}
		action(ctx, service)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	request := func(id int, values ...int) []byte {
		b := wire.AppendVarint(nil, 1, 1)
		b = wire.AppendVarint(b, 2, uint64(id))
		b = wire.AppendVarint(b, 3, 1)
		for _, value := range values {
			b = wire.AppendVarint(b, 4, uint64(value))
		}
		return b
	}
	handle := func(ctx command.Context, service *Service, path string, raw []byte) []byte {
		t.Helper()
		_, response, handled, err := service.Handle(ctx, path, raw)
		if err != nil || !handled {
			t.Fatalf("%s: handled=%v err=%v", path, handled, err)
		}
		return response
	}
	run(func(ctx command.Context, service *Service) {
		if err := service.state.ClearQuest(ctx, 46, 1); err != nil {
			t.Fatal(err)
		}
		if err := service.state.SelectQuest(ctx, 1, progress.QuestSelection{Difficulty: 2}); err != nil {
			t.Fatal(err)
		}
		for _, id := range catalog.Packs[1].MainQuestIDs {
			if err := service.state.ClearQuest(ctx, id, 1, 2); err != nil {
				t.Fatal(err)
			}
		}
		handle(ctx, service, "/QuestAccept", request(101))
		if err := service.ValidateQuestBattle(ctx, 1, []uint64{103}); err == nil {
			t.Fatal("unreached cave battle must not be authorized by static quest range alone")
		}
	})
	for _, step := range []struct {
		quest, next int
		values      []int
	}{
		{101, 102, []int{1011}}, {102, 103, []int{1021}},
		{103, 104, []int{1}}, {104, 105, []int{1}}, {105, 106, []int{1051, 1052}},
		{106, 107, []int{1}}, {107, 108, []int{1071}}, {108, 109, []int{1}},
		{109, 110, []int{1}}, {110, 111, []int{1101}}, {111, 112, []int{1111}}, {112, 0, []int{1}},
	} {
		run(func(ctx command.Context, service *Service) {
			if step.quest == 103 {
				if err := service.ValidateQuestBattle(ctx, 1, []uint64{103}); err != nil {
					t.Fatalf("accepted cave continuation must authorize its battle: %v", err)
				}
				entry := handle(ctx, service, "/PackInGameInfo", wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1))
				completed := questIDs(t, entry, 3)
				if !slices.Contains(completed, 101) || !slices.Contains(completed, 102) {
					t.Fatalf("re-entry lost the completed forest stages: %v", completed)
				}
			}
			handle(ctx, service, "/QuestUpdate", request(step.quest, step.values...))
			response := handle(ctx, service, "/QuestClear", request(step.quest))
			next, present, err := wire.Bytes(response, 2)
			if err != nil || !present {
				t.Fatalf("clear %d did not provide its continuation: %v", step.quest, err)
			}
			id, _, err := wire.Varint(next, 1)
			level, _, levelErr := wire.Varint(next, 4)
			if err != nil || id != uint64(step.next) || levelErr != nil || level != 0 {
				t.Fatalf("forest continuation must remain normal side quest %d: id=%d level=%d", step.next, id, level)
			}
		})
		run(func(ctx command.Context, service *Service) {
			rows := service.activeSideQuestWires(1)
			wantRows := 1
			if step.next == 0 {
				wantRows = 0
			}
			if len(rows) != wantRows {
				t.Fatalf("reconnect should restore %d continuations, got %d", wantRows, len(rows))
			}
			if len(rows) > 0 {
				id, _, err := wire.Varint(rows[0], 1)
				if err != nil || id != uint64(step.next) {
					t.Fatalf("reconnect restored id=%d; player should continue with %d", id, step.next)
				}
			}
			if selection, _ := service.state.Selection(1); selection.Difficulty != 2 || selection.QuestID != 0 {
				t.Fatalf("side quest changed main difficulty: %+v", selection)
			}
			gold := service.wallet.Snapshot(ctx).Gold
			items := service.inventory.All(ctx)
			handle(ctx, service, "/QuestClear", request(step.quest))
			if service.wallet.Snapshot(ctx).Gold != gold || len(service.activeSideQuestWires(1)) != wantRows || !slices.Equal(service.inventory.All(ctx), items) {
				t.Fatal("clear retry changed rewards or duplicated continuation")
			}
		})
	}
	run(func(ctx command.Context, service *Service) {
		if err := service.ValidateQuestBattle(ctx, 1, []uint64{103}); err == nil {
			t.Fatal("cleared cave objective must not authorize another quest battle")
		}
		for _, path := range []string{"/PackInGameInfo", "/QuestInfo"} {
			response := handle(ctx, service, path, wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1))
			field := 2
			if path == "/PackInGameInfo" {
				field = 3
			}
			completed := questIDs(t, response, field)
			for id := 101; id <= 112; id++ {
				// Both NPCInfo and the find-quest HUD offer a node when its prior
				// is completed but this node is absent from CompleteQuestIDs.
				if !slices.Contains(completed, id) {
					t.Fatalf("%s lost completed side stage %d", path, id)
				}
			}
		}
		gold := service.wallet.Snapshot(ctx).Gold
		items := service.inventory.All(ctx)
		for _, id := range []int{101, 112} {
			_, _, _, err := service.Handle(ctx, "/QuestAccept", request(id))
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("completed side stage %d must not be accepted again: %v", id, err)
			}
		}
		if service.wallet.Snapshot(ctx).Gold != gold || !slices.Equal(service.inventory.All(ctx), items) {
			t.Fatal("rejected acceptance changed side-quest rewards")
		}
		if err := service.state.ClearQuest(ctx, 1, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := service.state.SelectQuest(ctx, 1, progress.QuestSelection{QuestID: 2, Difficulty: 1}); err != nil {
			t.Fatal(err)
		}
		response := handle(ctx, service, "/QuestInfo", wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1))
		completed := questIDs(t, response, 2)
		if slices.Contains(completed, 46) || !slices.Contains(completed, 1) || !slices.Contains(completed, 112) {
			t.Fatalf("hard main clears and normal side clears must retain separate progress: %v", completed)
		}
	})
}

func questIDs(t *testing.T, response []byte, field int) []int {
	t.Helper()
	var ids []int
	if err := wire.Walk(response, func(value wire.Field) error {
		if value.Number != field {
			return nil
		}
		for raw := value.Value; len(raw) > 0; {
			id, count := binary.Uvarint(raw)
			if count <= 0 {
				return errors.New("invalid completed quest IDs")
			}
			ids = append(ids, int(id))
			raw = raw[count:]
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return ids
}
