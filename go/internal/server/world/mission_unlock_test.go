package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"testing"
)

func TestMissionPackOnlyUnlockUsesTicketAndLevelAuthorization(t *testing.T) {
	s := testService()
	s.squadLevel = func() (uint64, error) { return 3, nil }
	s.storyCatalog.Packs[709] = gamedata.StoryPack{ID: 709, Open: &gamedata.ContentOpenRule{SquadLevel: 4}}
	if s.MissionsUnlocked(709, 0) || s.MissionsUnlocked(999, 0) || s.MissionsUnlocked(0, 1) {
		t.Fatal("locked pack/invalid condition opened")
	}
	s.squadLevel = func() (uint64, error) { return 4, nil }
	if !s.MissionsUnlocked(709, 0) || !s.MissionsUnlocked(0, 0) {
		t.Fatal("authorized pack not opened")
	}
	s.storyCatalog.Packs[710] = gamedata.StoryPack{ID: 710, Open: &gamedata.ContentOpenRule{TicketID: 901}}
	var err error
	s.inventory, err = player.OpenInventory(stateio.NewMemory(), &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	if s.MissionsUnlocked(710, 0) {
		t.Fatal("missing ticket opened")
	}
	if _, err := s.inventory.GrantOnce("ticket", []gamedata.BattleReward{{Type: 19, ID: 901, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if !s.MissionsUnlocked(710, 0) {
		t.Fatal("ticket authorization ignored")
	}
}

func TestCollectionRewardsGrantChangedQuestAndPackOnlyOnNormal(t *testing.T) {
	s := testService()
	s.storyCatalog.Packs[709] = gamedata.StoryPack{ID: 709, Quests: map[int]gamedata.QuestDesign{49: {ID: 49, CollectionRewards: []gamedata.Reward{{Type: 17, ID: 9901, Count: 1}}}}}
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
	items, _, err := s.grantQuestRewards(709, 49, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != 9901 || items[0].Type != 17 {
		t.Fatalf("items=%+v", items)
	}
}
