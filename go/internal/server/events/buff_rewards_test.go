package events

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"path/filepath"
	"testing"
)

func TestPermanentBuffRewardOwnershipAndRestartReplay(t *testing.T) {
	store := stateio.NewMemory()
	design := map[uint64]gamedata.PictorialBuffStat{1: {Category: 1, StatType: 9, Value: 0.02}}
	s, err := OpenBuffRewards(store, design)
	if err != nil {
		t.Fatal(err)
	}
	reward := []gamedata.Reward{{Type: 63, ID: 1, Count: 1}}
	if err := s.GrantOnce("mission:1", reward); err != nil {
		t.Fatal(err)
	}
	s, err = OpenBuffRewards(store, design)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantOnce("mission:1", reward); err != nil {
		t.Fatal(err)
	}
	buffs, err := s.SnapshotBuffs()
	if err != nil || len(buffs) != 1 || buffs[0].Value != 0.02 || buffs[0].Category != 1 {
		t.Fatal("buff missing or doubled", buffs, err)
	}
	if err := s.GrantOnce("mission:1", []gamedata.Reward{{Type: 63, ID: 1, Count: 2}}); err == nil {
		t.Fatal("identity definition changed")
	}
	if err := s.GrantOnce("unknown", []gamedata.Reward{{Type: 63, ID: 999, Count: 1}}); err == nil {
		t.Fatal("unknown buff granted")
	}
	if err := s.GrantOnce("mission:2", reward); err != nil {
		t.Fatal(err)
	}
	buffs, err = s.SnapshotBuffs()
	if err != nil || buffs[0].Value != 0.04 {
		t.Fatal("distinct permanent rewards not accumulated", buffs, err)
	}
}

func TestPermanentBuffAndCurrencyAccountRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	design := map[uint64]gamedata.PictorialBuffStat{1: {Category: 1, StatType: 9, Value: 0.02}}
	eco, _, _ := economyFixture(t, repo, &economyGraph{})
	buffs, err := OpenBuffRewards(repo, design)
	if err != nil {
		t.Fatal(err)
	}
	eco.AttachBuffRewards(buffs)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = eco.Apply("buff batch", nil, []gamedata.Reward{{Type: 63, ID: 1, Count: 1}, {Type: 4, Count: 100}}); err != nil {
		t.Fatal(err)
	}
	_ = op.Rollback()
	repo.Close()
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	eco, _, wallet := economyFixture(t, repo, &economyGraph{})
	buffs, err = OpenBuffRewards(repo, design)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := buffs.SnapshotBuffs()
	if err != nil || len(stats) != 0 || wallet.Snapshot().Gold != 5 {
		t.Fatal("rollback retained buff or currency", stats, err)
	}
	eco.AttachBuffRewards(buffs)
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = eco.Apply("buff batch", nil, []gamedata.Reward{{Type: 63, ID: 1, Count: 1}, {Type: 4, Count: 100}}); err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	stats, err = buffs.SnapshotBuffs()
	if err != nil || len(stats) != 1 || stats[0].Value != 0.02 || wallet.Snapshot().Gold != 105 {
		t.Fatal("retry did not persist", stats, err)
	}
}

func TestEconomyBuffRewardChangesOwnedStatsWithoutInventory(t *testing.T) {
	store := stateio.NewMemory()
	eco, items, _ := economyFixture(t, store, &economyGraph{})
	buffs, err := OpenBuffRewards(store, map[uint64]gamedata.PictorialBuffStat{1: {Category: 1, StatType: 9, Value: 0.02}})
	if err != nil {
		t.Fatal(err)
	}
	eco.AttachBuffRewards(buffs)
	bundle, err := eco.Apply("mission:buff", nil, []gamedata.Reward{{Type: 63, ID: 1, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items.All()) != 0 {
		t.Fatal("buff became fake inventory item")
	}
	if _, present, _ := wire.Bytes(bundle, 1); present {
		t.Fatal("buff encoded as native inventory grant")
	}
	if _, present, _ := wire.Bytes(bundle, 6); !present {
		t.Fatal("buff reward view missing")
	}
	replay, err := eco.Apply("mission:buff", nil, []gamedata.Reward{{Type: 63, ID: 1, Count: 1}})
	if err != nil || !bytes.Equal(bundle, replay) {
		t.Fatal("economy buff replay changed", err)
	}
	stats, err := buffs.SnapshotBuffs()
	if err != nil || len(stats) != 1 || stats[0].Value != 0.02 {
		t.Fatal("permanent buff repeated or missing", stats, err)
	}
}
