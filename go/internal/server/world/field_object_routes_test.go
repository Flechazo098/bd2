package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/deck"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sqliteFieldService(t *testing.T, store *fieldRewardFailStore) *Service {
	t.Helper()
	s := testService()
	var err error
	s.state, err = progress.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.state.SetActivePackID(21); err != nil {
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
	s.monsterStore = store
	return s
}

func fieldBatchRequest(ids ...int) []byte {
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21)
	for _, id := range ids {
		obj := wire.AppendVarint(wire.AppendVarint(nil, 3, 101), 4, uint64(id))
		request = wire.AppendBytes(request, 3, obj)
	}
	return request
}

func TestFieldBatchSQLiteAtomicResetAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	store := &fieldRewardFailStore{Repository: repo}
	s := sqliteFieldService(t, store)
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	s.monsterNow = func() time.Time { return now }
	objects := map[int]gamedata.FieldRewardObject{}
	for _, id := range []int{1001, 1002} {
		objects[id] = gamedata.FieldRewardObject{ID: id, GroupID: 101, MapID: 211, Type: 6, ResetType: 0, DropCount: 1, Ratios: []uint64{1}, Rewards: []gamedata.BattleReward{{Type: 3, Count: 50}}}
	}
	designs := map[int]gamedata.FieldObjectDesign{21: {Objects: objects}}
	s.WithFieldObjects(designs)
	if _, _, _, err := s.handleFieldObjectRewardList(fieldBatchRequest(1001, 9999)); err == nil {
		t.Fatal("invalid later batch object accepted")
	}
	if s.wallet.Snapshot().FreeJewelry != 0 {
		t.Fatal("invalid batch settled earlier object")
	}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if _, _, _, err := s.handleFieldObjectRewardList(fieldBatchRequest(1001, 1002)); err == nil {
		t.Fatal("marker failure accepted")
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
	store.Repository, store.fail = repo, false
	s = sqliteFieldService(t, store).WithFieldObjects(designs)
	s.monsterNow = func() time.Time { return now }
	if s.wallet.Snapshot().FreeJewelry != 0 {
		t.Fatal("rollback persisted partial currency")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	code, reply, _, err := s.handleFieldObjectRewardList(fieldBatchRequest(1001, 1002, 1001))
	if err != nil || code != 260 {
		t.Fatalf("batch code=%d err=%v", code, err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	bundle, _, _ := wire.Bytes(reply, 1)
	var total uint64
	if err := wire.Walk(bundle, func(f wire.Field) error {
		if f.Number == 1 {
			count, _, e := wire.Varint(f.Value, 4)
			total += count
			return e
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if total != 100 || s.wallet.Snapshot().FreeJewelry != 100 {
		t.Fatalf("batch delta=%d wallet=%+v", total, s.wallet.Snapshot())
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Repository = repo
	s = sqliteFieldService(t, store).WithFieldObjects(designs)
	s.monsterNow = func() time.Time { return now }
	_, reply, _, err = s.handleFieldObjectRewardList(fieldBatchRequest(1001, 1002))
	if err != nil {
		t.Fatal(err)
	}
	bundle, _, _ = wire.Bytes(reply, 1)
	if len(bundle) != 0 || s.wallet.Snapshot().FreeJewelry != 100 {
		t.Fatal("restart duplicate paid again")
	}
	respawnRequest := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 21), 3, 101)
	_, before, _, err := s.handleFieldObjectRespawn(respawnRequest)
	if err != nil {
		t.Fatal(err)
	}
	if fieldWireCount(t, before, 1) != 2 {
		t.Fatal("premature respawn cleared opened state")
	}
	now = now.Add(24 * time.Hour)
	_, after, _, err := s.handleFieldObjectPreview(wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, 21))
	if err != nil || fieldWireCount(t, after, 1) != 0 {
		t.Fatalf("reset preview=%x err=%v", after, err)
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.handleFieldObjectRewardList(fieldBatchRequest(1001, 1002)); err != nil {
		t.Fatal(err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	if s.wallet.Snapshot().FreeJewelry != 200 {
		t.Fatal("new reset period did not award")
	}
}

func fieldWireCount(t *testing.T, raw []byte, number int) int {
	t.Helper()
	count := 0
	if err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number == number {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestFieldEffectsSQLitePersistenceRepeatAndBattleConsumption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	store := &fieldRewardFailStore{Repository: repo}
	s := sqliteFieldService(t, store)
	designs := map[int]gamedata.FieldObjectDesign{21: {Objects: map[int]gamedata.FieldRewardObject{
		1001: {ID: 1001, GroupID: 101, MapID: 211, ResetType: 1, BuffID: 1},
		1002: {ID: 1002, GroupID: 101, MapID: 211, ResetType: 1, BuffID: 2},
		1003: {ID: 1003, GroupID: 101, MapID: 211, ResetType: 1, MonsterID: 9},
	}}}
	buffs := map[uint64]gamedata.FieldBuffDesign{1: {ID: 1, Type: 0, Time: 2}, 2: {ID: 2, Type: 0, Time: 3}}
	attach := func(s *Service) {
		s.WithFieldObjects(designs)
		s.fieldBuffs = buffs
		s.monsterLoader = func(int) ([]gamedata.FieldMonsterDesign, error) {
			return []gamedata.FieldMonsterDesign{{ID: 9, GroupID: 7, Type: 3, BattleDeck: 99}}, nil
		}
	}
	attach(s)
	for _, id := range []int{1001, 1002, 1003} {
		op, err := repo.BeginOperation()
		if err != nil {
			t.Fatal(err)
		}
		response, err := s.openFieldObjectResponse(21, 101, id)
		if err != nil {
			t.Fatal(err)
		}
		if id == 1003 {
			monster, _, _ := wire.Bytes(response, 4)
			mid, _, _ := wire.Varint(monster, 1)
			if mid != 9 {
				t.Fatal("dynamic monster response missing")
			}
		} else if fieldWireCount(t, response, 2) != 1 {
			t.Fatal("buff response missing")
		}
		if err := op.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Repository = repo
	s = sqliteFieldService(t, store)
	attach(s)
	rows, err := s.state.FieldBuffs()
	if err != nil || len(rows) != 1 {
		t.Fatalf("buff rows=%v err=%v", rows, err)
	}
	id, _, _ := wire.Varint(rows[0], 1)
	count, _, _ := wire.Varint(rows[0], 2)
	if id != 2 || count != 3 {
		t.Fatal("same-type replacement or persisted duration lost")
	}
	response, err := s.openFieldObjectResponse(21, 101, 1002)
	if err != nil || fieldWireCount(t, response, 2) != 0 {
		t.Fatal("repeat reapplied buff")
	}
	for _, identity := range []string{"battle-a", "battle-a", "battle-b", "battle-c"} {
		op, err := repo.BeginOperation()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ConsumeFieldBattleBuff(identity); err != nil {
			t.Fatal(err)
		}
		if err := op.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = s.state.FieldBuffs()
	if err != nil || len(rows) != 0 {
		t.Fatal("battle retries decremented incorrectly or buff never expired")
	}
	snapshot, err := s.loadMonsterState()
	if err != nil || len(snapshot.Monsters) != 1 {
		t.Fatal("dynamic monster spawn lost on restart")
	}
}

func TestInstalledFieldChestAndHealingObjectSQLiteSettlement(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for installed field settlement regression")
	}
	version := "20260923193640"
	chests, err := gamedata.LoadFieldObjects(root, version, 21)
	if err != nil {
		t.Fatal(err)
	}
	traps, err := gamedata.LoadFieldObjects(root, version, 1)
	if err != nil {
		t.Fatal(err)
	}
	buffs, err := gamedata.LoadFieldBuffDesign(root, version)
	if err != nil {
		t.Fatal(err)
	}
	var chest, healer gamedata.FieldRewardObject
	for _, obj := range chests.Objects {
		if len(obj.Rewards) > 0 && (chest.ID == 0 || obj.ID < chest.ID) {
			chest = obj
		}
	}
	for _, obj := range traps.Objects {
		if buff, ok := buffs[uint64(obj.BuffID)]; ok && buff.Type == 3 && (healer.ID == 0 || obj.ID < healer.ID) {
			healer = obj
		}
	}
	if chest.ID == 0 || healer.ID == 0 {
		t.Fatal("current authored chest/healer graphs missing")
	}
	repo, err := accountstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	store := &fieldRewardFailStore{Repository: repo}
	s := sqliteFieldService(t, store).WithFieldObjects(map[int]gamedata.FieldObjectDesign{21: chests, 1: traps})
	s.fieldBuffs = buffs
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	response, err := s.openFieldObjectResponse(21, chest.GroupID, chest.ID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _, _ := wire.Bytes(response, 1)
	if len(bundle) == 0 {
		t.Fatal("installed chest gave no authored rewards")
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	s.storyCatalog.Packs[1] = gamedata.StoryPack{ID: 1, Quests: map[int]gamedata.QuestDesign{1: {ID: 1}}, MainQuestIDs: []int{1}}
	if s.packs == nil {
		s.packs = map[int]map[int]gamedata.QuestDesign{}
	}
	s.packs[1] = s.storyCatalog.Packs[1].Quests
	if err := s.state.SetActivePackID(1); err != nil {
		t.Fatal(err)
	}
	s.setCurrentPack(1)
	s.characters, err = player.OpenCharacterStore(store, []player.Character{{ID: 350, InvenIndex: 77, Level: 1, HP: 50}}, s.inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.characters.AttachMaxHealth(func(player.Character) (uint64, error) { return 100, nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.characters.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := s.characters.SetCurrentHealth(77, 50); err != nil {
		t.Fatal(err)
	}
	s.decks, err = deck.NewStore(deck.Seed{Version: s.seed.Version, FieldCharControlDeckType: 1, FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: 77}}})
	if err != nil {
		t.Fatal(err)
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	response, err = s.openFieldObjectResponse(1, healer.GroupID, healer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	hp, err := s.characters.CurrentHealth(77)
	if err != nil || hp != 80 || fieldWireCount(t, response, 2) != 1 || fieldWireCount(t, response, 3) != 1 {
		t.Fatalf("installed 30%% healer hp=%d response=%x err=%v", hp, response, err)
	}
	if _, err := s.openFieldObjectResponse(1, healer.GroupID, healer.ID); err != nil {
		t.Fatal(err)
	}
	hp, _ = s.characters.CurrentHealth(77)
	if hp != 80 {
		t.Fatal("healing retry applied twice")
	}
}
