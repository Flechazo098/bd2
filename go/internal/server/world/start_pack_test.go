package world

import (
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
)

func TestStartingChapterPolicyPersistsWithoutReplacingTutorialSeed(t *testing.T) {
	storage := stateio.NewMemory()
	state, err := progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	s := testService()
	s.state = state
	s.storyCatalog.Packs[1] = gamedata.StoryPack{ID: 1, MainQuestIDs: []int{8}, Quests: map[int]gamedata.QuestDesign{8: {ID: 8}}, Open: &gamedata.ContentOpenRule{TicketID: 71}}
	if err := s.ConfigureStartPack(1, true); err != nil {
		t.Fatal(err)
	}
	if s.seed.PackID != 21 || !s.packUnlocked(1) || s.tutorialRosterRestricted() {
		t.Fatal("entry policy overwrote tutorial seed or blocked entry")
	}
	if id, err := s.LastPlayedPackID(); err != nil || id != 1 {
		t.Fatalf("destination %d, %v", id, err)
	}
	selection, ok := s.state.Selection(1)
	if !ok || selection.QuestID != 8 || selection.Difficulty != 0 {
		t.Fatalf("initial quest %+v", selection)
	}
	reopened, err := progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	s.state = reopened
	if err := s.ConfigureStartPack(21, false); err != nil {
		t.Fatal(err)
	}
	if s.startingPack() != 1 {
		t.Fatal("changed server policy moved existing account")
	}
}

func TestInitialPurchaseUsesSelectedEntryChapter(t *testing.T) {
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
	s.collection, err = player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.storyCatalog.Packs[1] = gamedata.StoryPack{ID: 1, MainQuestIDs: []int{8}, Quests: map[int]gamedata.QuestDesign{8: {ID: 8}}, BuyRewards: []gamedata.Reward{{Type: 12, Count: 17}}}
	if err := s.ConfigureStartPack(1, true); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureInitialPackPurchase(); err != nil {
		t.Fatal(err)
	}
	if _, owned := s.collection.Grant("pack-purchase:1"); !owned || s.wallet.CatalystBalance() != 17 {
		t.Fatal("entry purchase not committed")
	}
	if _, owned := s.collection.Grant("pack-purchase:21"); owned {
		t.Fatal("tutorial purchase incorrectly granted")
	}
}
