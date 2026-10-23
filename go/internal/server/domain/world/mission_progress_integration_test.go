//go:build integration

package world

import (
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type stubEconomy struct{}

func (stubEconomy) Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error) {
	return nil, nil
}

type stubEventFieldPacks struct{}

func (stubEventFieldPacks) ResolveEventFieldPack(command.Context, int) (gamedata.EventFieldPack, bool, error) {
	return gamedata.EventFieldPack{}, false, nil
}
func (stubEventFieldPacks) ListEventFieldPacks(command.Context) ([]gamedata.EventFieldPack, error) {
	return nil, nil
}
func (stubEventFieldPacks) FieldObjectEventPeriod(int) (string, int64, error) {
	return "test", 0, nil
}

func recordMissionEvents(service *Service) *[]conditions.Event {
	events := []conditions.Event{}
	service.AttachMissionProgress(func(_ command.Context, event conditions.Event) error {
		events = append(events, event)
		return nil
	})
	return &events
}

func fieldObjectMissionScenario(t *testing.T, pack int) (command.Context, *Service, gamedata.FieldObjectDesign) {
	t.Helper()
	ctx, service, _, _ := eventMonsterIntegration(t, pack)
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	source := gamedata.NewSource(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err := service.AttachFieldObjectRuntime(source, service.fieldReset); err != nil {
		t.Fatal(err)
	}
	service.packs = map[int]map[int]gamedata.QuestDesign{pack: service.storyCatalog.Packs[pack].Quests}
	design, err := source.FieldObjects(pack)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, service, design
}

func claimFieldObject(t *testing.T, ctx command.Context, service *Service, pack, seq uint64, obj gamedata.FieldRewardObject) {
	t.Helper()
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, pack), 3, uint64(obj.GroupID)), 4, uint64(obj.ID))
	code, _, handled, err := service.Handle(ctx, "/FieldObjectReward", request)
	if err != nil || !handled || code != 29 {
		t.Fatalf("field object claim %d: handled=%v code=%d err=%v", obj.ID, handled, code, err)
	}
}

// GameData pack9 green pot33011/group303 has a buff and no loot group, so it
// takes the buff branch of openFieldObjectResponse. The claim must still count
// as one McCollectionGet and must not count again after the state reloads.
func TestFieldObjectClaimReportsCollectionOnce(t *testing.T) {
	ctx, service, design := fieldObjectMissionScenario(t, 9)
	obj, found := design.Objects[33011]
	if !found || obj.BuffID == 0 {
		t.Fatalf("green pot fixture changed: found=%v object=%+v", found, obj)
	}
	events := recordMissionEvents(service)
	claimFieldObject(t, ctx, service, 9, 1, obj)
	if len(*events) != 1 || (*events)[0].Type != conditions.CollectionGet || (*events)[0].Count != 1 {
		t.Fatalf("claim events = %+v, want one McCollectionGet", *events)
	}
	var err error
	service.state, err = progress.OpenStore(ctx, stateio.RootStore{})
	if err != nil {
		t.Fatal(err)
	}
	claimFieldObject(t, ctx, service, 9, 2, obj)
	if len(*events) != 1 {
		t.Fatalf("repeated claim added events: %+v", *events)
	}
}

// GameData pack2 type5 objects have no buff, monster or loot, so they take
// openFieldObject's empty-loot branch, which must report the same
// McCollectionGet.
func TestFieldObjectWithoutLootReportsCollection(t *testing.T) {
	ctx, service, design := fieldObjectMissionScenario(t, 2)
	service.eventFieldPacks = stubEventFieldPacks{}
	var candidate gamedata.FieldRewardObject
	for _, obj := range design.Objects {
		if len(obj.Rewards) == 0 && obj.BuffID == 0 && obj.MonsterID == 0 && (obj.QuestID != 0 || obj.Type == 5 || obj.Type == 6) {
			candidate = obj
			break
		}
	}
	if candidate.ID == 0 {
		t.Fatal("GameData has no non-buff field object without loot")
	}
	events := recordMissionEvents(service)
	claimFieldObject(t, ctx, service, 2, 1, candidate)
	if len(*events) != 1 || (*events)[0].Type != conditions.CollectionGet || (*events)[0].Count != 1 {
		t.Fatalf("claim events = %+v, want one McCollectionGet", *events)
	}
}

// grantFieldMonster reports one catch regardless of the reward item count.
func TestGrantFieldMonsterRecordsFieldRewardMonsterCatch(t *testing.T) {
	ctx, service, _, _ := eventMonsterIntegration(t, 9)
	service.researchEconomy = stubEconomy{}
	events := recordMissionEvents(service)
	monster := gamedata.FieldMonsterDesign{ID: 1, Reward: gamedata.Reward{Type: 4, Count: 5}}
	if _, err := service.grantFieldMonster(ctx, 9, monster, "mission-catch"); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 || (*events)[0].Type != conditions.FieldRewardMonsterCatch || (*events)[0].Count != 1 {
		t.Fatalf("catch events = %+v, want one McFieldRewardMonsterCatch", *events)
	}
}

func TestPackEntryReportsCondition(t *testing.T) {
	ctx, service, items, _ := eventMonsterIntegration(t, 9)
	service.packs = map[int]map[int]gamedata.QuestDesign{9: service.storyCatalog.Packs[9].Quests}
	service.inventory = items
	events := recordMissionEvents(service)
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 9)
	if _, _, handled, err := service.Handle(ctx, "/PackInGameInfo", request); err != nil || !handled {
		t.Fatalf("pack entry: handled=%v err=%v", handled, err)
	}
	if len(*events) != 1 || (*events)[0].Type != conditions.EnterPackInField || (*events)[0].Sub != 9 || (*events)[0].Count != 1 {
		t.Fatalf("pack entry events = %+v, want one McEnterPackInField for pack 9", *events)
	}
}
