package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// CharTable10140 is the temporary blacksmith joining pack1 quest11.
// Its TalentSkillTable group901 has row1 only; omitted field8 defaults to zero
// in the client and causes a lookup of the nonexistent group901/row0.
func TestInstalledStoryPartyInitialTalentPersistsAndEncodes(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	catalog, err := gamedata.LoadStoryCharacterCatalog(root, "20260923193640", []int{1}, 996000)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	open := func() (*accountstate.Repository, *Service) {
		t.Helper()
		storage, err := accountstate.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = storage.Close() })
		inventory, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		s := testService()
		s.storyRoster = catalog
		s.characters, err = player.OpenCharacterStore(storage, nil, inventory, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.characters.EnsurePersisted(); err != nil {
			t.Fatal(err)
		}
		return storage, s
	}
	storage, s := open()
	party, err := s.ResolveStoryParty(1, 11)
	if err != nil {
		t.Fatal(err)
	}
	var joined player.Character
	for _, c := range party {
		if c.ID == 10140 {
			joined = c
		}
	}
	if joined.InvenIndex == 0 || joined.TalentLevel != 1 {
		t.Fatalf("temporary blacksmith has invalid initial talent: %+v", joined)
	}
	if err = s.characters.SetCurrentHealth(joined.InvenIndex, 7); err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}
	_, s = open()
	characters, _, err := s.resolveActivePartyWires(1, 11)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range characters {
		id, _, err := wire.Varint(data, 2)
		if err != nil {
			t.Fatal(err)
		}
		if id != joined.ID {
			continue
		}
		level, present, err := wire.Varint(data, 8)
		if err != nil || !present || level != 1 {
			t.Fatalf("CharDBInfo talent_level=%d present=%v err=%v", level, present, err)
		}
		hp, _, err := wire.Varint(data, 3)
		if err != nil || hp != 7 {
			t.Fatalf("reconnect changed temporary HP=%d err=%v", hp, err)
		}
		return
	}
	t.Fatal("temporary blacksmith omitted from party CharDBInfo")
}

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
