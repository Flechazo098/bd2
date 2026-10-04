package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"errors"
	"path/filepath"
	"testing"
)

type fieldRewardFailStore struct {
	*accountstate.Repository
	fail bool
}

func (s *fieldRewardFailStore) PutEntry(domain, bucket, key string, payload []byte) error {
	if s.fail && bucket == "field_rewards" {
		return errors.New("forced field reward marker failure")
	}
	return s.Repository.PutEntry(domain, bucket, key, payload)
}
func TestFieldChestAtomicAwardRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { repo.Close() }()
	store := &fieldRewardFailStore{Repository: repo}
	s := testService()
	s.state, err = progress.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.state.SetActivePackID(21); err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(store, player.Currency{FreeJewelry: 100})
	if err != nil {
		t.Fatal(err)
	}
	s.inventory, err = player.OpenInventory(store, s.starter)
	if err != nil {
		t.Fatal(err)
	}
	s.WithFieldObjects(map[int]gamedata.FieldObjectDesign{21: {Objects: map[int]gamedata.FieldRewardObject{1001: {ID: 1001, MapID: 211, GroupID: 101, Type: 2, ResetType: 1, DropCount: 1, Ratios: []uint64{1}, Rewards: []gamedata.BattleReward{{Type: 3, Count: 50}}}}}})
	if _, err = s.wallet.GrantQuestOnce("baseline", nil); err != nil {
		t.Fatal(err)
	}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if _, err = s.openFieldObject(21, 101, 1001); err == nil {
		t.Fatal("failed persistence returned success")
	}
	if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Repository = repo
	store.fail = false
	s.wallet, err = player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	s.inventory, err = player.OpenInventory(store, s.starter)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.state.FieldRewardOpened(21, 1001, "once")
	if err != nil || opened || s.wallet.Snapshot().FreeJewelry != 100 || len(s.inventory.All()) != 0 {
		t.Fatalf("partial state after rollback opened=%v wallet=%+v err=%v", opened, s.wallet.Snapshot(), err)
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.openFieldObject(21, 101, 1001); err != nil {
		op.Rollback()
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if s.wallet.Snapshot().FreeJewelry != 150 {
		t.Fatal("retry after rollback did not award")
	}
	obj := s.fieldObjects[21].Objects[1001]
	obj.ID = 1002
	obj.Rewards = []gamedata.BattleReward{{Type: 5, ID: 1013, Count: 142}}
	s.fieldObjects[21].Objects[1002] = obj
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if _, err = s.openFieldObject(21, 101, 1002); err == nil {
		t.Fatal("inventory marker failure returned success")
	}
	if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Repository = repo
	store.fail = false
	s.inventory, err = player.OpenInventory(store, s.starter)
	if err != nil {
		t.Fatal(err)
	}
	opened, err = s.state.FieldRewardOpened(21, 1002, "once")
	if err != nil || opened || len(s.inventory.All()) != 0 {
		t.Fatalf("inventory partially committed after rollback opened=%v items=%v err=%v", opened, s.inventory.All(), err)
	}
}

func TestFieldChestDeltaAndPersistentRepeat(t *testing.T) {
	store := stateio.NewMemory()
	s := testService()
	var err error
	s.state, err = progress.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.state.SetActivePackID(21); err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(store, player.Currency{FreeJewelry: 100})
	if err != nil {
		t.Fatal(err)
	}
	s.inventory, err = player.OpenInventory(store, s.starter)
	if err != nil {
		t.Fatal(err)
	}
	s.WithFieldObjects(map[int]gamedata.FieldObjectDesign{21: {Objects: map[int]gamedata.FieldRewardObject{1001: {ID: 1001, MapID: 211, GroupID: 101, Type: 2, ResetType: 1, DropCount: 1, Ratios: []uint64{10}, Rewards: []gamedata.BattleReward{{Type: 3, Count: 50}}}}}})
	req := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21), 3, 101), 4, 1001)
	code, res, _, err := s.handleFieldObjectReward(req)
	if err != nil || code != 29 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	bundle, _, _ := wire.Bytes(res, 1)
	item, _, _ := wire.Bytes(bundle, 1)
	delta, _, _ := wire.Varint(item, 4)
	if delta != 50 || s.wallet.Snapshot().FreeJewelry != 150 {
		t.Fatalf("delta=%d wallet=%+v", delta, s.wallet.Snapshot())
	}
	s.state, err = progress.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	// Quest selection difficulty is independent of the pack's opened chests.
	// Switching it must neither hide the chest nor create a second grant.
	if err = s.state.SelectQuest(21, progress.QuestSelection{QuestID: 1, Difficulty: 2}); err != nil {
		t.Fatal(err)
	}
	req = wire.AppendVarint(req, 1, 999)
	_, res, _, err = s.handleFieldObjectReward(req)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _, _ = wire.Bytes(res, 1)
	if len(bundle) != 0 || s.wallet.Snapshot().FreeJewelry != 150 {
		t.Fatal("repeat awarded")
	}
	_, res, _, err = s.handleFieldObjectInfo(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21))
	if err != nil {
		t.Fatal(err)
	}
	info, _, _ := wire.Bytes(res, 1)
	id, _, _ := wire.Varint(info, 1)
	if id != 1001 {
		t.Fatalf("opened id=%d", id)
	}
	if _, err = s.openFieldObject(21, 102, 1001); err == nil {
		t.Fatal("wrong group accepted")
	}
}
