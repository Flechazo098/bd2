package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

func TestStoryCatalogUsesTicketsAndQuestLinksAcrossIndependentPacks(t *testing.T) {
	s := testService()
	s.seed.PackID = 701
	storage := stateio.NewMemory()
	var err error
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
	quests := map[int]gamedata.QuestDesign{90: {ID: 90, NextQuestID: 7}, 7: {ID: 7, PriorQuestID: 90, Rewards: [5][]gamedata.Reward{{{Type: 19, ID: 888, Count: 1}}}}, 50: {ID: 50, Type: 1}}
	s.storyCatalog = &gamedata.StoryCatalog{Packs: map[int]gamedata.StoryPack{
		701: {ID: 701, Quests: quests, MainQuestIDs: []int{7, 90}},
		405: {ID: 405, Open: &gamedata.ContentOpenRule{TicketID: 888}, Quests: map[int]gamedata.QuestDesign{33: {ID: 33}}, MainQuestIDs: []int{33}},
	}}
	s.packs = map[int]map[int]gamedata.QuestDesign{701: quests, 405: s.storyCatalog.Packs[405].Quests}
	if err := s.orderMainQuests(); err != nil {
		t.Fatal(err)
	}
	if s.firstUnclearedQuestFor(701) != 90 || s.canClear(701, 7) || s.canClear(701, 50) || s.packUnlocked(405) {
		t.Fatal("numeric ordering or ticket gate bypassed")
	}
	if err := s.state.ClearQuest(90, 701); err != nil {
		t.Fatal(err)
	}
	if !s.canClear(701, 7) || s.nextQuestFor(701, 90) != 7 {
		t.Fatal("design next/prior ignored")
	}
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 7), 3, 701)
	_, response, _, err := s.Handle("/QuestClear", request)
	if err != nil {
		t.Fatal(err)
	}
	if !s.packCompleteFor(701) || !s.packUnlocked(405) {
		t.Fatal("main terminal ticket did not unlock independent story")
	}
	if s.state.QuestCleared(50, 701) {
		t.Fatal("side quest fabricated")
	}
	var ids []uint64
	if err := wire.Walk(response, func(f wire.Field) error {
		if f.Number == 11 {
			id, _, _ := wire.Varint(f.Value, 1)
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 405 || ids[1] != 701 {
		t.Fatalf("terminal updates %v", ids)
	}
}

func TestInitialPackPurchaseUsesDesignAndDurableIdentity(t *testing.T) {
	s := testService()
	s.seed.PackID = 707
	storage := stateio.NewMemory()
	var err error
	s.inventory, err = player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(storage, player.Currency{Catalyst: 9})
	if err != nil {
		t.Fatal(err)
	}
	s.collection, err = player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.storyCatalog = &gamedata.StoryCatalog{Packs: map[int]gamedata.StoryPack{707: {ID: 707, BuyRewards: []gamedata.Reward{{Type: 12, Count: 17}, {Type: 19, ID: 88, Count: 1}}}}}
	for i := 0; i < 2; i++ {
		if err := s.EnsureInitialPackPurchase(); err != nil {
			t.Fatal(err)
		}
	}
	if s.wallet.CatalystBalance() != 26 || len(s.inventory.All()) != 1 {
		t.Fatal("purchase reward repeated or hardcoded")
	}
	if _, owned := s.collection.Grant("pack-purchase:707"); !owned {
		t.Fatal("missing purchase marker")
	}
	if !s.wallet.WasGranted("pack-purchase:707:currency") {
		t.Fatal("missing currency marker")
	}
}
