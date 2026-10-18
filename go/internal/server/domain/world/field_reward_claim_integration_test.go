//go:build integration

package world

import (
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

// Native FieldPacket addresses rewards by pack/group/object, not current map.
// GameData 20261012190548: pack9 green pot33011/group303 has buff19,
// restoring 30% of maximum HP; its map92 metadata is also used by the HUD.
func TestGreenPotClaimAfterMapChangeHealsOnlyOnce(t *testing.T) {
	ctx, service, items, setMap := eventMonsterIntegration(t, 9)
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	source := gamedata.NewSource(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err := service.AttachFieldObjectRuntime(source, service.fieldReset); err != nil {
		t.Fatal(err)
	}
	service.packs = map[int]map[int]gamedata.QuestDesign{9: service.storyCatalog.Packs[9].Quests}
	maxHP, err := service.characters.MaxHealth(ctx, 1)
	if err != nil || maxHP < 10 {
		t.Fatalf("field character maximum HP: %d, %v", maxHP, err)
	}
	if err := service.characters.SetCurrentHealth(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	setMap(92)
	setMap(93)
	claim := func(group uint64) ([]byte, error) {
		t.Helper()
		request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 9), 3, group), 4, 33011)
		code, response, handled, err := service.Handle(ctx, "/FieldObjectReward", request)
		if err == nil && (!handled || code != 29) {
			t.Fatalf("native pot response unavailable: handled=%v code=%d", handled, code)
		}
		return response, err
	}
	if _, err := claim(302); err == nil {
		t.Fatal("another object's group must not authorize this pot")
	}
	if hp, err := service.characters.CurrentHealth(ctx, 1); err != nil || hp != 1 {
		t.Fatalf("rejected claim changed health: hp=%d err=%v", hp, err)
	}
	response, err := claim(303)
	if err != nil {
		t.Fatalf("native object claim after entering map93: %v", err)
	}
	if hp, err := service.characters.CurrentHealth(ctx, 1); err != nil || hp != 1+maxHP*3/10 {
		t.Fatalf("green pot must restore 30%% maximum HP once: hp=%d max=%d err=%v", hp, maxHP, err)
	}
	var buffID uint64
	if err := wire.Walk(response, func(f wire.Field) error {
		if f.Number == 2 && f.Type == 2 {
			var err error
			buffID, _, err = wire.Varint(f.Value, 1)
			return err
		}
		return nil
	}); err != nil || buffID != 19 {
		t.Fatalf("native client cannot apply pot effect: buff=%d err=%v", buffID, err)
	}
	if err := service.characters.SetCurrentHealth(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	service.state, err = progress.OpenStore(ctx, stateio.RootStore{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claim(303); err != nil {
		t.Fatalf("retry after state reload must acknowledge consumed pot: %v", err)
	}
	if hp, err := service.characters.CurrentHealth(ctx, 1); err != nil || hp != 1 {
		t.Fatalf("retry healed from the same pot twice: hp=%d err=%v", hp, err)
	}
	if got := items.All(ctx); len(got) != 0 {
		t.Fatal("healing pot granted inventory loot")
	}
	opened, err := service.openedFieldObjects(ctx, 9)
	if err != nil || len(opened) != 1 || opened[0] != 33011 {
		t.Fatalf("reloaded preview must retain consumed pot: opened=%v err=%v", opened, err)
	}
}
