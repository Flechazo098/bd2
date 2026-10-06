package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/deck"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Source: GameData 20260923193640 CharTable10140 is the temporary blacksmith
// joining pack1 quest11, and Proto/Net/CharDBInfo supplies talent_level field8.
// Its TalentSkillTable group901 has row1 only; omitted field8 defaults to zero
// in the client and causes a lookup of the nonexistent group901/row0.
func TestInstalledStoryCharactersInitialTalentPersistsAndEncodes(t *testing.T) {
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
	party, err := s.resolveStoryCharacters(1, 11)
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
	before := s.characters.RawAll()
	repeated, err := s.resolveStoryCharacters(1, 11)
	if err != nil || !reflect.DeepEqual(repeated, party) || !reflect.DeepEqual(s.characters.RawAll(), before) {
		t.Fatalf("repeated temporary resolution changed identity or character state: err=%v", err)
	}
	if err = s.characters.SetCurrentHealth(joined.InvenIndex, 7); err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}
	_, s = open()
	characters, decks, err := s.resolveActivePartyWires(1, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(decks) != 0 {
		t.Fatal("temporary talent character fabricated an unsaved battle deck")
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

// Source: GameData 20260923193640 QuestTable1 quests 13..16 use CharGroup102
// while their cosmetic StoryCharGroup changes. The saved five-player party
// and positions below implement the user's policy for ordinary story battles.
func TestInstalledQuest13ClearKeepsSavedBattlePartyAcrossReopen(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	roster, err := gamedata.LoadStoryCharacterCatalog(root, "20260923193640", []int{1}, 996000)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := gamedata.LoadStoryCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	seed := &player.Starter{Version: "2.35.10", Characters: []player.Character{
		{InvenIndex: 101, ID: 10, HP: 71, Level: 30, CostumeID: 101, UseCostume: 1001, TalentLevel: 1, TalentExp: 13, Exp: 123},
		{InvenIndex: 102, ID: 20, HP: 72, Level: 30, CostumeID: 202, UseCostume: 1002, TalentLevel: 1, TalentExp: 14, Exp: 124},
		{InvenIndex: 103, ID: 130, HP: 73, Level: 30, CostumeID: 1301, UseCostume: 1003, TalentLevel: 1, TalentExp: 15, Exp: 125},
		{InvenIndex: 104, ID: 140, HP: 74, Level: 30, CostumeID: 1401, UseCostume: 1004, TalentLevel: 1},
		{InvenIndex: 105, ID: 350, HP: 75, Level: 30, CostumeID: 3501, UseCostume: 1005, TalentLevel: 1},
	}, Costumes: []player.Costume{
		{InvenIndex: 1001, ID: 101, UseChar: 101}, {InvenIndex: 1002, ID: 202, UseChar: 102},
		{InvenIndex: 1003, ID: 1301, UseChar: 103}, {InvenIndex: 1004, ID: 1401, UseChar: 104},
		{InvenIndex: 1005, ID: 3501, UseChar: 105},
	}}
	// Inventory order, wire order, slot sequence, and grid position are distinct.
	saved := []deck.DeckEntry{
		{CharacterInvenIndex: 105, CostumeInvenIndex: 0, Slot: 3},
		{CharacterInvenIndex: 103, CostumeInvenIndex: 11, Slot: 1},
		{CharacterInvenIndex: 101, CostumeInvenIndex: 3, Slot: 5},
		{CharacterInvenIndex: 104, CostumeInvenIndex: ^uint64(0), Slot: 2},
		{CharacterInvenIndex: 102, CostumeInvenIndex: 8, Slot: 4},
	}
	path := filepath.Join(t.TempDir(), "state.db")
	open := func() (*accountstate.Repository, *Service) {
		t.Helper()
		repo, err := accountstate.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Close() })
		s := testService()
		s.seed.PackID, s.seed.StartQuestID, s.startingPackID = 1, 1, 1
		s.storyRoster, s.storyCatalog, s.starter = roster, catalog, seed
		s.quests = catalog.Packs[1].Quests
		s.state, err = progress.OpenStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		s.inventory, err = player.OpenInventory(repo, seed)
		if err != nil {
			t.Fatal(err)
		}
		s.characters, err = player.OpenCharacterStore(repo, seed.Characters, s.inventory, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = s.characters.EnsurePersisted(); err != nil {
			t.Fatal(err)
		}
		s.decks, err = deck.OpenStore(repo, deck.Seed{Version: "2.35.10", FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: 104}}})
		if err != nil {
			t.Fatal(err)
		}
		return repo, s
	}
	repo, s := open()
	saveStoryTestDeck(t, s.decks, saved)
	for id := 1; id < 13; id++ {
		if err := s.state.ClearQuest(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.state.UpdateQuest(wire.AppendVarint(selectionRequest(13, 1, 0), 4, 1)); err != nil {
		t.Fatal(err)
	}
	beforeResolve := s.characters.RawAll()
	assertStoryTestBattleParty(t, s, saved)
	if !reflect.DeepEqual(s.characters.RawAll(), beforeResolve) {
		t.Fatal("battle party resolution materialized temporary characters")
	}
	temporary, err := s.resolveStoryCharacters(1, 13)
	if err != nil || len(temporary) == 0 {
		t.Fatalf("quest13 temporary talent characters=%+v err=%v", temporary, err)
	}
	for i, c := range temporary {
		if err := s.characters.SetCurrentHealth(c.InvenIndex, uint64(7+i)); err != nil {
			t.Fatal(err)
		}
	}
	before := s.characters.RawAll()
	for id := 13; id <= 15; id++ {
		chars, decks, err := s.resolveActivePartyWires(1, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(chars) != len(temporary) || !reflect.DeepEqual(decodeStoryTestDeck(t, decks), saved) {
			t.Fatalf("quest%d displaced a player or moved positions", id)
		}
		assertStoryTestBattleParty(t, s, saved)
		op, err := repo.BeginOperation()
		if err != nil {
			t.Fatal(err)
		}
		_, response, handled, err := s.Handle("/QuestClear", selectionRequest(uint64(id), 1, 0))
		if err != nil || !handled {
			_ = op.Rollback()
			t.Fatalf("quest%d clear handled=%v err=%v", id, handled, err)
		}
		if err := op.Commit(); err != nil {
			t.Fatal(err)
		}
		echoed, joins := storyTestClearParty(t, response)
		if !reflect.DeepEqual(echoed, saved) || len(joins) != 0 {
			t.Fatalf("quest%d changed choices/positions or fabricated join UI: deck=%+v want=%+v joins=%v", id, echoed, saved, joins)
		}
		assertStoryTestBattleParty(t, s, saved)
		if !reflect.DeepEqual(s.characters.RawAll(), before) {
			t.Fatalf("quest%d transition changed character stats, talents, health, or identities", id)
		}
		if err := repo.Close(); err != nil {
			t.Fatal(err)
		}
		repo, s = open()
		if !s.state.QuestCleared(id, 1) || s.firstUnclearedQuestFor(1) != id+1 {
			t.Fatalf("quest%d clear did not survive SQLite reopen", id)
		}
		assertStoryTestBattleParty(t, s, saved)
		if !reflect.DeepEqual(s.characters.RawAll(), before) {
			t.Fatalf("quest%d reopen changed character state", id)
		}
	}
}

// Source: installed GameData CharGroup rows choose an adjacent unchanged
// temporary cast; the server's instance policy preserves its identity and HP.
func TestInstalledAdjacentStoryCharactersRetainHealth(t *testing.T) {
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
	party, err := s.resolveStoryCharacters(21, first)
	if err != nil {
		t.Fatal(err)
	}
	if party[0].InvenIndex != legacy.InvenIndex {
		t.Fatal("existing seeded temporary identity was not reused")
	}
	if err := characters.SetCurrentHealth(party[0].InvenIndex, 7); err != nil {
		t.Fatal(err)
	}
	next, err := s.resolveStoryCharacters(21, second)
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
