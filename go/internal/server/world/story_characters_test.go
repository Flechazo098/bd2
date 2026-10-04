package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"os"
	"reflect"
	"testing"
)

func TestInstalledAdjacentStoryPartyRetainsHealth(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	catalog, err := gamedata.LoadStoryCharacterCatalog(root, "20260923193640", []int{21})
	if err != nil {
		t.Fatal(err)
	}
	first, second := 0, 0
	for quest := 1; quest < 38; quest++ {
		a, err := catalog.Characters(21, quest)
		if err != nil {
			t.Fatal(err)
		}
		b, err := catalog.Characters(21, quest+1)
		if err != nil {
			t.Fatal(err)
		}
		if len(a) > 0 && reflect.DeepEqual(a, b) {
			first, second = quest, quest+1
			break
		}
	}
	if first == 0 {
		t.Fatal("no adjacent authored same party fixture")
	}
	storage := stateio.NewMemory()
	inventory, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	designs, err := catalog.Characters(21, first)
	if err != nil {
		t.Fatal(err)
	}
	legacy := player.Character{InvenIndex: 123, ID: designs[0].CharacterID, HP: designs[0].HP, Level: designs[0].Level}
	characters, err := player.OpenCharacterStore(storage, []player.Character{legacy}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachMaxHealth(func(c player.Character) (uint64, error) { return c.HP, nil }); err != nil {
		t.Fatal(err)
	}
	s := testService()
	s.characters = characters
	s.storyRoster = catalog
	party, err := s.ResolveStoryParty(21, first)
	if err != nil {
		t.Fatal(err)
	}
	if party[0].InvenIndex != legacy.InvenIndex {
		t.Fatal("existing seeded temporary identity was not reused")
	}
	if err := characters.SetCurrentHealth(party[0].InvenIndex, 7); err != nil {
		t.Fatal(err)
	}
	next, err := s.ResolveStoryParty(21, second)
	if err != nil {
		t.Fatal(err)
	}
	if next[0].InvenIndex != party[0].InvenIndex || next[0].HP != 7 {
		t.Fatalf("quest%d→%d reset party hp or index: %+v→%+v", first, second, party[0], next[0])
	}
	if len(characters.RawAll()) != len(party) {
		t.Fatal("adjacent quest duplicated temporary instances")
	}
}
