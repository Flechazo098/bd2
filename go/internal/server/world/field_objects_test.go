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
	"reflect"
	"testing"
)

func fieldEquipmentCatalog(t *testing.T) *gamedata.EquipmentGachaCatalog {
	t.Helper()
	options := []gamedata.OptionGroup{{ID: 31, Choices: []gamedata.WeightedOption{{ID: 4, Weight: 1}}}}
	d, err := gamedata.NewFirstGachaDesign(gamedata.GachaGroupDesign{ID: 1, GachaSubType: 3, TenTimeGachaID: 2}, 2, 1,
		&gamedata.FirstGachaRewardGroup{ID: 3, DropType: 1, DropCount: 1, Entries: []gamedata.FirstGachaRewardEntry{{ItemType: 10, ItemID: 77, Count: 1, Weight: 1}}}, nil,
		map[uint64]gamedata.EquipmentDesign{77: {ID: 77, Grade: 3, Main: options, Sub: options, Private: options}})
	if err != nil {
		t.Fatal(err)
	}
	return d.EquipmentCatalog()
}

func TestFieldEquipmentTransactionRollbackAndRetry(t *testing.T) {
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
	if err := s.state.SetActivePackID(21); err != nil {
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
	s.equipment, err = player.OpenEquipmentInventory(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.wallet.GrantQuestOnce("baseline", nil); err != nil {
		t.Fatal(err)
	}
	obj := gamedata.FieldRewardObject{ID: 1001, MapID: 211, GroupID: 101, ResetType: 1, DropType: 1, Ratios: []uint64{100, 100, 100}, Rewards: []gamedata.BattleReward{{Type: 3, Count: 50}, {Type: 9, ID: 88, Count: 1}, {Type: 10, ID: 77, Count: 2}}}
	s.WithFieldObjects(map[int]gamedata.FieldObjectDesign{21: {Objects: map[int]gamedata.FieldRewardObject{1001: obj}, Equipment: fieldEquipmentCatalog(t)}})
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if _, err := s.openFieldObject(21, 101, 1001); err == nil {
		t.Fatal("failed marker accepted")
	}
	if err := op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Repository = repo
	store.fail = false
	s.state, err = progress.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	s.inventory, err = player.OpenInventory(store, s.starter)
	if err != nil {
		t.Fatal(err)
	}
	s.equipment, err = player.OpenEquipmentInventory(store)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.state.FieldRewardOpened(21, 1001, "once")
	if err != nil || opened || len(s.equipment.All()) != 0 || len(s.inventory.All()) != 0 || s.wallet.Snapshot().FreeJewelry != 100 {
		t.Fatalf("partial transaction: opened=%v equipment=%+v items=%+v wallet=%+v err=%v", opened, s.equipment.All(), s.inventory.All(), s.wallet.Snapshot(), err)
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.openFieldObject(21, 101, 1001); err != nil {
		op.Rollback()
		t.Fatal(err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(s.equipment.All()) != 2 || len(s.inventory.All()) != 1 || s.wallet.Snapshot().FreeJewelry != 150 {
		t.Fatal("retry lost grants or grant receipts")
	}
}

func TestFieldIndependentEquipmentAndBoxPersistAcrossRestart(t *testing.T) {
	store := stateio.NewMemory()
	s := testService()
	var err error
	s.state, err = progress.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.SetActivePackID(21); err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	s.inventory, err = player.OpenInventory(store, s.starter)
	if err != nil {
		t.Fatal(err)
	}
	s.equipment, err = player.OpenEquipmentInventory(store)
	if err != nil {
		t.Fatal(err)
	}
	obj := gamedata.FieldRewardObject{ID: 1001, MapID: 211, GroupID: 101, Type: 8, ResetType: 1, DropType: 1, Ratios: []uint64{100, 100, 100}, Rewards: []gamedata.BattleReward{{Type: 10, ID: 77, Count: 2}, {Type: 9, ID: 88, Count: 1}, {Type: 3, Count: 50}}}
	s.WithFieldObjects(map[int]gamedata.FieldObjectDesign{21: {Objects: map[int]gamedata.FieldRewardObject{1001: obj}, Equipment: fieldEquipmentCatalog(t)}})
	bundle, err := s.openFieldObject(21, 101, 1001)
	if err != nil {
		t.Fatal(err)
	}
	entries := s.equipment.All()
	if len(entries) != 2 || entries[0].InvenIndex == entries[1].InvenIndex {
		t.Fatalf("equipment=%+v", entries)
	}
	for _, entry := range entries {
		if len(entry.MainOption) != 1 || len(entry.SubOption) != 1 || entry.PrivateOption == nil || !reflect.DeepEqual(entry.Rank, []uint64{0, 0, 0}) {
			t.Fatalf("missing equipment metadata=%+v", entry)
		}
	}
	var eqCount, boxCount int
	if err := wire.Walk(bundle, func(f wire.Field) error {
		if f.Number == 4 {
			eqCount++
			if !reflect.DeepEqual(f.Value, player.EquipmentWire(entries[eqCount-1])) {
				t.Error("equipment wire lost metadata/index")
			}
		}
		if f.Number == 1 {
			kind, _, _ := wire.Varint(f.Value, 3)
			if kind == 9 {
				boxCount++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if eqCount != 2 || boxCount != 1 || s.wallet.Snapshot().FreeJewelry != 50 {
		t.Fatalf("equipment=%d box=%d wallet=%+v", eqCount, boxCount, s.wallet.Snapshot())
	}
	s.state, err = progress.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	s.equipment, err = player.OpenEquipmentInventory(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.SelectQuest(21, progress.QuestSelection{QuestID: 1, Difficulty: 2}); err != nil {
		t.Fatal(err)
	}
	bundle, err = s.openFieldObject(21, 101, 1001)
	if err != nil || len(bundle) != 0 || !reflect.DeepEqual(entries, s.equipment.All()) {
		t.Fatalf("duplicate after restart: bundle=%x err=%v", bundle, err)
	}
	obj.ID = 1002
	obj.DropType = 0
	obj.DropCount = 1
	obj.Rewards = []gamedata.BattleReward{{Type: 3, Count: 50}, {Type: 12345, ID: 1, Count: 1}}
	obj.Ratios = []uint64{100, 0}
	s.fieldObjects[21].Objects[1002] = obj
	if _, err := s.openFieldObject(21, 101, 1002); err == nil {
		t.Fatal("malformed unselected branch accepted")
	}
	if s.wallet.Snapshot().FreeJewelry != 50 || len(s.equipment.All()) != 2 {
		t.Fatal("malformed branch changed stores")
	}
}

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
