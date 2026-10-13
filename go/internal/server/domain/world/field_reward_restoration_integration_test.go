//go:build integration

package world

import (
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

// GameData 20261008155037 pack5 object5120 is the searchable HiddenBox in
// map51, group12: weekly reset and one182-unit crafting-material draw.
// FieldPacket.WeeklyResetFieldRewardObjects restores this group at reset;
// resetting does not grant anything until the player collects it again.
func TestHiddenInvestigationRewardBecomesCollectibleOnceAtWeeklyReset(t *testing.T) {
	ctx, service, source := fieldQuestIntegration(t, 5)
	schedule, err := gamedata.LoadFieldResetSchedule(source.Root(), source.Version())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachFieldObjectRuntime(source, schedule); err != nil {
		t.Fatal(err)
	}
	service.packs = map[int]map[int]gamedata.QuestDesign{5: service.storyCatalog.Packs[5].Quests}
	service.wallet, err = assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service.inventory, err = assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := gamedata.LoadItemStackDesign(source.Root(), source.Version())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.inventory.AttachItemStacks(stacks); err != nil {
		t.Fatal(err)
	}
	if err := service.state.SaveUserPosition(ctx, wire.AppendString(wire.AppendVarint(nil, 2, 5), 3, `{"MapId":51}`)); err != nil {
		t.Fatal(err)
	}
	reset := time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Millisecond)
	service.monsterNow = func() time.Time { return now }
	seq := uint64(0)
	handle := func(path string, fields []byte) []byte {
		t.Helper()
		seq++
		_, response, handled, err := service.Handle(ctx, path, append(wire.AppendVarint(nil, 1, seq), fields...))
		if err != nil || !handled {
			t.Fatalf("%s: handled=%v err=%v", path, handled, err)
		}
		return response
	}
	materials := func() uint64 {
		var total uint64
		for _, item := range service.inventory.All(ctx) {
			if item.Type == 8 && (item.ID == 301 || item.ID == 401 || item.ID == 204) {
				total += item.Count
			}
		}
		return total
	}
	assertCollected := func(want bool) {
		t.Helper()
		response := handle("/FieldObjectPreview", wire.AppendVarint(nil, 2, 5))
		found := false
		if err := wire.Walk(response, func(f wire.Field) error {
			if f.Number == 1 && f.Type == 2 {
				id, _, err := wire.Varint(f.Value, 1)
				if err != nil {
					return err
				}
				found = found || id == 5120
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if found != want {
			t.Fatalf("preview reported collected=%v want%v at%v", found, want, now)
		}
	}
	collect := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 2, 5), 3, 12), 4, 5120)
	assertCollected(false)
	handle("/FieldObjectReward", collect)
	assertCollected(true)
	if total := materials(); total != 182 {
		t.Fatalf("first collection granted%d materials, want182", total)
	}
	handle("/FieldObjectReward", collect)
	if total := materials(); total != 182 {
		t.Fatalf("same-period collection duplicated materials:%d", total)
	}
	now = reset
	assertCollected(false)
	if total := materials(); total != 182 {
		t.Fatalf("weekly reset granted uncollected materials:%d", total)
	}
	handle("/FieldObjectReward", collect)
	assertCollected(true)
	if total := materials(); total != 364 {
		t.Fatalf("next-period collection granted%d materials, want364", total)
	}
	handle("/FieldObjectReward", collect)
	if total := materials(); total != 364 {
		t.Fatalf("next-period retry duplicated materials:%d", total)
	}
}
