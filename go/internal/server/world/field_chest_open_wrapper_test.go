package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/server/stateio"
)

// Player.log's failing map-3 chest returned OPEN box 240001 as ItemDBInfo;
// PlayerController then loaded its intentionally absent ResourceIcon. This
// regression settles that exact selected branch using installed design data.
func TestInstalledFieldChestOpenWrapperProtocolAndSQLiteRollback(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for the chest crash regression")
	}
	design, err := gamedata.LoadFieldObjects(root, "20260923193640", 1)
	if err != nil {
		t.Fatal(err)
	}
	chest := design.Objects[921]
	if chest.MapID != 3 || chest.GroupID != 23 {
		t.Fatal("crash fixture no longer matches current chest design")
	}
	var failingBranch gamedata.BattleReward
	for _, reward := range chest.Rewards {
		if reward.Type == 9 && reward.ID == 240001 {
			failingBranch = reward
		}
	}
	if failingBranch.Count != 1 {
		t.Fatal("authored OPEN equipment branch missing")
	}
	// Condition on the real branch selected at 09:15:23; select the first
	// authored child deterministically so rollback/restart assertions are exact.
	chest.Rewards, chest.Ratios = []gamedata.BattleReward{failingBranch}, []uint64{1}
	design.Objects[921] = chest
	design.RewardGraph.SetSampler(func(uint64) (uint64, error) { return 0, nil })
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { repo.Close() }()
	store := &fieldRewardFailStore{Repository: repo}
	service := func() *Service {
		s := sqliteFieldService(t, store).WithFieldObjects(map[int]gamedata.FieldObjectDesign{1: design})
		s.storyCatalog.Packs[1] = gamedata.StoryPack{ID: 1, Quests: map[int]gamedata.QuestDesign{1: {ID: 1}}, MainQuestIDs: []int{1}}
		s.packs = map[int]map[int]gamedata.QuestDesign{1: s.storyCatalog.Packs[1].Quests}
		if err := s.state.SetActivePackID(1); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := service()
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1), 3, 23), 4, 921)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if _, _, _, err := s.handleFieldObjectReward(request); err == nil {
		t.Fatal("failed opened marker accepted")
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
	s = service()
	opened, err := s.state.FieldRewardOpened(1, 921, "once")
	if err != nil || opened || len(s.equipment.All()) != 0 || len(s.inventory.All()) != 0 {
		t.Fatal("rolled back chest left equipment, wrapper, or marker")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	code, response, _, err := s.handleFieldObjectReward(request)
	if err != nil || code != 29 {
		t.Fatalf("code=%d error=%v", code, err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	bundle, _, _ := wire.Bytes(response, 1)
	if fieldWireCount(t, bundle, 1) != 0 || fieldWireCount(t, bundle, 4) != 1 || len(s.inventory.All()) != 0 {
		t.Fatal("OPEN wrapper leaked into ItemDBInfo/inventory instead of EquipDBInfo")
	}
	equipment, found, err := wire.Bytes(bundle, 4)
	base, _, _ := wire.Bytes(equipment, 5)
	id, _, _ := wire.Varint(base, 1)
	index, _, _ := wire.Varint(equipment, 1)
	if err != nil || !found || id != 10010 || index == 0 || len(s.equipment.All()) != 1 || s.equipment.All()[0].InvenIndex != index {
		t.Fatalf("wrong authored equipment id=%d index=%d", id, index)
	}
	for _, field := range []int{3, 4, 5} {
		if _, found, err := wire.Bytes(base, field); err != nil || !found {
			t.Fatalf("equipment missing client metadata field %d", field)
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
	s = service()
	_, response, _, err = s.handleFieldObjectReward(request)
	bundle, _, _ = wire.Bytes(response, 1)
	if err != nil || len(bundle) != 0 || len(s.equipment.All()) != 1 || len(s.inventory.All()) != 0 {
		t.Fatal("restart/retry repeated equipment or created a wrapper")
	}
}
