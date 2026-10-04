package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

func selectionRequest(quest, pack, level uint64) []byte {
	out := wire.AppendVarint(nil, 1, 1)
	out = wire.AppendVarint(out, 2, quest)
	out = wire.AppendVarint(out, 3, pack)
	return wire.AppendVarint(out, 4, level)
}
func TestDifficultySelectionPersistsAndRewardsRemainIndependent(t *testing.T) {
	s := testService()
	storage := stateio.NewMemory()
	var err error
	s.state, err = progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	s.inventory, err = player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	s.questDifficulties = map[int]map[int]bool{21: {1: true, 2: true}}
	for id, design := range s.storyCatalog.Packs[21].Quests {
		design.Rewards[0] = []gamedata.Reward{{Type: 2, Count: 10}}
		design.Rewards[1] = []gamedata.Reward{{Type: 2, Count: 20}}
		s.storyCatalog.Packs[21].Quests[id] = design
	}
	if _, _, _, err := s.Handle("/QuestAccept", selectionRequest(1, 21, 1)); err == nil {
		t.Fatal("hard accepted before normal completion")
	}
	for id := uint64(1); id <= 3; id++ {
		if _, _, _, err := s.Handle("/QuestClear", selectionRequest(id, 21, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := s.Handle("/QuestAccept", selectionRequest(2, 21, 1)); err == nil {
		t.Fatal("arbitrary hard quest accepted")
	}
	if _, _, _, err := s.Handle("/QuestAccept", selectionRequest(1, 21, 1)); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, _, _, err := s.Handle("/QuestClear", selectionRequest(1, 21, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if s.wallet.Snapshot().Jewelry != 50 {
		t.Fatalf("reward isolation/replay balance %+v", s.wallet.Snapshot())
	}
	reopened, err := progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	s.state = reopened
	if selection, ok := reopened.Selection(21); !ok || selection.Difficulty != 1 || selection.QuestID != 2 {
		t.Fatalf("selection lost %+v", selection)
	}
	_, response, _, err := s.Handle("/PackInGameInfo", wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21))
	if err != nil {
		t.Fatal(err)
	}
	quest, _, _ := wire.Bytes(response, 2)
	id, _, _ := wire.Varint(quest, 1)
	level, _, _ := wire.Varint(quest, 4)
	if id != 2 || level != 1 {
		t.Fatalf("restored id%d level%d", id, level)
	}
	if _, _, _, err := s.Handle("/QuestGiveUp", selectionRequest(2, 21, 0)); err != nil {
		t.Fatal(err)
	}
	if !s.state.QuestCleared(1, 21, 1) {
		t.Fatal("abandon erased hard clear")
	}
	if _, _, _, err := s.Handle("/QuestAccept", selectionRequest(2, 21, 1)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/QuestAccept", selectionRequest(1, 21, 4)); err == nil {
		t.Fatal("unsupported table difficulty accepted")
	}
}
func TestSideQuestRemainsIndependentFromMainDifficulty(t *testing.T) {
	s := testService()
	pack := s.storyCatalog.Packs[21]
	pack.Quests[50] = gamedata.QuestDesign{ID: 50, Type: 1}
	s.storyCatalog.Packs[21] = pack
	if _, _, _, err := s.Handle("/QuestAccept", selectionRequest(50, 21, 0)); err != nil {
		t.Fatal(err)
	}
	if first := s.firstUnclearedQuestFor(21); first != 1 {
		t.Fatalf("side replaced main%d", first)
	}
	if _, _, _, err := s.Handle("/QuestClear", selectionRequest(50, 21, 0)); err != nil {
		t.Fatal(err)
	}
	if !s.state.QuestCleared(50, 21) || s.state.QuestCleared(1, 21) {
		t.Fatal("side clear affected main")
	}
}

func TestSelectionCannotSwitchWhileBattleIsActive(t *testing.T) {
	s := testService()
	if err := s.AttachBattleActive(func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/QuestAccept", "/QuestGiveUp"} {
		if _, _, _, err := s.Handle(path, selectionRequest(1, 21, 0)); err == nil {
			t.Fatalf("%s switched active battle", path)
		}
	}
	if _, _, _, err := s.Handle("/PackInGameInfo", wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21)); err == nil {
		t.Fatal("pack switch allowed during battle")
	}
}
