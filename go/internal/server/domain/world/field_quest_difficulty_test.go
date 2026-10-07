//go:build integration

package world

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/wire"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/stateio"
)

func fieldQuestIntegration(t *testing.T, pack int) (command.Context, *Service, *gamedata.Source) {
	t.Helper()
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			t.Error(err)
		}
	})
	root := versions.Resolve("data/resources/GameData")
	catalog, err := gamedata.LoadStoryCatalog(root, versions.GameDataVersion)
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
	tx, err := repo.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil {
			t.Error(err)
		}
	})
	ctx := command.Context{Identity: command.Identity{AccountID: t.Name(), SessionID: "authenticated"}, State: tx}
	state, err := progress.OpenStore(ctx, stateio.RootStore{})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SelectQuest(ctx, pack, progress.QuestSelection{Difficulty: 2}); err != nil {
		t.Fatal(err)
	}
	service := &Service{seed: Seed{PackID: pack}, activePack: pack, state: state, storyCatalog: catalog}
	return ctx, service, gamedata.NewSource(root, versions.GameDataVersion)
}

// Pack 2's authored side-quest crates appear after quest 208; main-quest
// crates appear after quest 16 at the selected difficulty. Changing main
// difficulty must preserve side-crate positions while resetting main crates.
func TestAuthoredFieldActionsUseTheirQuestDifficulty(t *testing.T) {
	ctx, service, source := fieldQuestIntegration(t, 2)
	design, err := source.FieldObjects(2)
	if err != nil {
		t.Fatal(err)
	}
	service.WithFieldObjects(map[int]gamedata.FieldObjectDesign{2: design})
	move := func(id int) error {
		obj := design.Actions[id]
		userPosition := wire.AppendBytes(wire.AppendVarint(nil, 2, 2), 3, []byte(fmt.Sprintf(`{"MapId":%d}`, obj.MapID)))
		if err := service.state.SaveUserPosition(ctx, userPosition); err != nil {
			return err
		}
		request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 2, 2), 3, uint64(obj.GroupID)), 4, uint64(id))
		request = wire.AppendVarint(request, 1, 1)
		request = wire.AppendBytes(request, 5, wire.AppendVarint(nil, 1, uint64(obj.MapID)))
		_, _, _, err := service.handleFieldObjectPosition(ctx, request)
		return err
	}
	if err := service.state.SelectQuest(ctx, 2, progress.QuestSelection{Difficulty: 0}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{2102, 181} {
		obj := design.Actions[id]
		if err := service.state.ClearQuest(ctx, obj.QuestID, 2, 0); err != nil {
			t.Fatal(err)
		}
		if err := move(id); err != nil {
			t.Fatalf("completed normal quest must enable crate %d: %v", id, err)
		}
	}
	if err := service.state.SelectQuest(ctx, 2, progress.QuestSelection{Difficulty: 2}); err != nil {
		t.Fatal(err)
	}
	info, err := service.fieldActionInfo(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := nestedMessages(info, 2)
	if err != nil || len(rows) != 1 {
		t.Fatalf("only the side crate may retain its drop position after changing main difficulty: rows=%d err=%v", len(rows), err)
	}
	id, _, err := wire.Varint(rows[0], 1)
	if err != nil || id != 2102 {
		t.Fatalf("changing main difficulty must retain side crate 2102 and reset main crate 181: id=%d err=%v", id, err)
	}
	if err := move(2102); err != nil {
		t.Fatalf("completed side quest 208 must enable its movable crate at main difficulty 2: %v", err)
	}
	if err := move(181); err == nil {
		t.Fatal("main quest 16 completed only at normal must not enable its difficulty-2 crate")
	}
	if err := service.state.ClearQuest(ctx, 16, 2, 2); err != nil {
		t.Fatal(err)
	}
	if err := move(181); err != nil {
		t.Fatalf("difficulty-2 main completion must enable its crate: %v", err)
	}
	service.state, err = progress.OpenStore(ctx, stateio.RootStore{})
	if err != nil {
		t.Fatal(err)
	}
	info, err = service.fieldActionInfo(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = nestedMessages(info, 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("reconnect must retain both positions moved after their quests completed: rows=%d err=%v", len(rows), err)
	}
}
