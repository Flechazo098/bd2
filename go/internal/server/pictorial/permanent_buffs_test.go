package pictorial

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

func TestPermanentBuffsRefreshThroughAccountSnapshot(t *testing.T) {
	store := stateio.NewMemory()
	design := map[uint64]gamedata.PictorialBuffStat{1: {Category: 1, StatType: 9, Value: 0.02}}
	rewards, err := events.OpenBuffRewards(store, design)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Design: &gamedata.PictorialDesign{}, Owned: &ownedState{}}
	service.AttachPermanentBuffs(rewards.SnapshotBuffs)
	_, before, err := service.Snapshot()
	if err != nil || len(before) != 0 {
		t.Fatal("buff present before ownership", err)
	}
	if err := rewards.GrantOnce("mission", []gamedata.Reward{{Type: 63, ID: 1, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	_, after, err := service.Snapshot()
	if err != nil || len(after) != 1 || after[0].Value != 0.02 {
		t.Fatal("new buff missing from live stats", after, err)
	}
	code, body, handled, err := service.Handle("/AllCharRefresh", wire.AppendVarint(nil, 1, 1))
	if err != nil || code != 165 || !handled || len(body) == 0 {
		t.Fatal("native buff refresh missing", err)
	}
	restored, err := events.OpenBuffRewards(store, design)
	if err != nil {
		t.Fatal(err)
	}
	service.AttachPermanentBuffs(restored.SnapshotBuffs)
	_, after, err = service.Snapshot()
	if err != nil || len(after) != 1 || after[0].Category != 1 {
		t.Fatal("relogin buff lost", after, err)
	}
}
