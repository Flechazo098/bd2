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

// Quest13 and quest14 share CharGroup102, but their field StoryCharGroup grows
// from three to four costume designs. Owning the whole field cast used to add
// a sixth battle member and roll back this otherwise completed quest clear.
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
	}, Costumes: []player.Costume{
		{InvenIndex: 1001, ID: 101, UseChar: 101}, {InvenIndex: 1002, ID: 202, UseChar: 102},
		{InvenIndex: 1003, ID: 1301, UseChar: 103}, {InvenIndex: 1004, ID: 1401, UseChar: 104},
	}}
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
	for id := 1; id < 13; id++ {
		if err := s.state.ClearQuest(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	update := wire.AppendVarint(selectionRequest(13, 1, 0), 4, 1)
	if _, err := s.state.UpdateQuest(update); err != nil {
		t.Fatal(err)
	}
	party, err := s.ResolveStoryParty(1, 13)
	if err != nil || len(party) != 3 { // Two temporaries and the only initial field choice.
		t.Fatalf("initial story party=%+v err=%v", party, err)
	}
	var saved []deck.DeckEntry
	for _, c := range party {
		if !player.IsStoryCharacter(c) {
			continue
		}
		if err := s.characters.SetCurrentHealth(c.InvenIndex, 7+uint64(len(saved))); err != nil {
			t.Fatal(err)
		}
		saved = append(saved, deck.DeckEntry{CharacterInvenIndex: c.InvenIndex, CostumeInvenIndex: uint64(len(saved) * 2), Slot: uint64(len(saved) + 1)})
	}
	for _, c := range seed.Characters[:3] {
		saved = append(saved, deck.DeckEntry{CharacterInvenIndex: c.InvenIndex, CostumeInvenIndex: uint64(len(saved) * 2), Slot: uint64(len(saved) + 1)})
	}
	if err := s.decks.SetStoryParty(saved); err != nil {
		t.Fatal(err)
	}
	before := s.characters.RawAll()
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	_, response, handled, err := s.Handle("/QuestClear", selectionRequest(13, 1, 0))
	if err != nil || !handled {
		_ = op.Rollback()
		t.Fatalf("quest13 clear handled=%v err=%v", handled, err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	var echoed []deck.DeckEntry
	var joins int
	if err := wire.Walk(response, func(f wire.Field) error {
		if f.Number == 5 {
			joins++
		}
		if f.Number == 4 {
			index, _, _ := wire.Varint(f.Value, 1)
			position, _, _ := wire.Varint(f.Value, 2)
			slot, _, _ := wire.Varint(f.Value, 3)
			echoed = append(echoed, deck.DeckEntry{CharacterInvenIndex: index, CostumeInvenIndex: position, Slot: slot})
		}
		return nil
	}); err != nil || !reflect.DeepEqual(echoed, saved) || joins != 0 {
		t.Fatalf("clear changed saved battle choices/positions or repeated join UI: deck=%+v want=%+v joins=%d err=%v", echoed, saved, joins, err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	_, s = open()
	if !s.state.QuestCleared(13, 1) || s.firstUnclearedQuestFor(1) != 14 {
		t.Fatal("quest13 clear did not survive SQLite reopen")
	}
	if !reflect.DeepEqual(s.decks.CurrentDeck(), saved) || !reflect.DeepEqual(s.characters.RawAll(), before) {
		t.Fatal("quest transition or reopen changed party, positions, character stats, talents, or health")
	}
	party, err = s.ResolveStoryParty(1, 14)
	if err != nil || len(party) != 5 {
		t.Fatalf("reopened quest14 battle party=%+v err=%v", party, err)
	}
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
