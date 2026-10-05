package events

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"path/filepath"
	"testing"
)

func avatarDesign() *gamedata.AvatarRewardDesign {
	return &gamedata.AvatarRewardDesign{Sets: map[uint64][]gamedata.BattleReward{1: {{Type: 49, ID: 10001, Count: 1}, {Type: 50, ID: 7, Count: 1}, {Type: 61, ID: 2, Count: 1}}}, Items: map[uint64]map[uint64]bool{49: {10001: true}, 50: {7: true}, 61: {2: true}}}
}
func TestAvatarSetBatchPersistsMembersAndRollsBackWithAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	eco, _, _ := economyFixture(t, repo, &economyGraph{})
	eco.AttachAvatarRewards(avatarDesign())
	rewards := []gamedata.Reward{{Type: 62, ID: 1, Count: 1}, {Type: 4, Count: 10}, {Type: 8, ID: 1000, Count: 2}}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = eco.Apply("mission", nil, rewards); err != nil {
		t.Fatal(err)
	}
	_ = op.Rollback()
	repo.Close()
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	eco, items, wallet := economyFixture(t, repo, &economyGraph{})
	eco.AttachAvatarRewards(avatarDesign())
	if len(items.All()) != 0 || wallet.Snapshot().Gold != 5 {
		t.Fatal("rollback retained avatar grant")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	reply, err := eco.Apply("mission", nil, rewards)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(items.All()) != 4 || wallet.Snapshot().Gold != 15 {
		t.Fatal("mixed grant did not persist", items.All())
	}
	for _, item := range items.All() {
		if item.Type == 62 {
			t.Fatal("synthetic set stored")
		}
	}
	types := map[uint64]bool{}
	_ = wire.Walk(reply, func(f wire.Field) error {
		if f.Number == 1 {
			typ, _, _ := wire.Varint(f.Value, 3)
			types[typ] = true
		}
		return nil
	})
	for _, typ := range []uint64{49, 50, 61, 4, 8} {
		if !types[typ] {
			t.Fatal("native member missing", typ)
		}
	}
	eco, items, wallet = economyFixture(t, repo, &economyGraph{})
	eco.AttachAvatarRewards(avatarDesign())
	replay, err := eco.Apply("mission", nil, rewards)
	if err != nil || !bytes.Equal(reply, replay) || len(items.All()) != 4 || wallet.Snapshot().Gold != 15 {
		t.Fatal("restart replay duplicated", err)
	}
	_, avatar, _, err := eco.OwnedItemInfo("/AvatarInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	_ = wire.Walk(avatar, func(f wire.Field) error {
		if f.Number == 2 {
			count++
		}
		return nil
	})
	if count != 3 {
		t.Fatal("relogin lost avatar ownership", count)
	}
	before := wallet.Snapshot().Gold
	if _, err = eco.Apply("unknown", nil, []gamedata.Reward{{Type: 4, Count: 50}, {Type: 62, ID: 999, Count: 1}}); err == nil || wallet.Snapshot().Gold != before || len(items.All()) != 4 {
		t.Fatal("unknown set partially granted")
	}
}
