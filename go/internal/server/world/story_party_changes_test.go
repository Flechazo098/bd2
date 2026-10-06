package world

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/deck"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/wire"
)

// Source: GameData 20260923193640 supplies all main-quest edges and temporary
// CharGroup designs; client QuestPacket feeds QuestClear.CharInfo into its join
// UI. User policy keeps the saved player battle deck on every ordinary edge.
func TestInstalledQuestClearProjectsPartyChangesOnly(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for authored party transition regression")
	}
	catalog, err := gamedata.LoadStoryCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	var packs []int
	for id := range catalog.Packs {
		packs = append(packs, id)
	}
	sort.Ints(packs)
	roster, err := gamedata.LoadStoryCharacterCatalog(root, "20260923193640", packs, 996000)
	if err != nil {
		t.Fatal(err)
	}
	edges, changedEdges := 0, 0
	for _, pack := range packs {
		t.Run(fmt.Sprintf("pack%d", pack), func(t *testing.T) {
			design := catalog.Packs[pack]
			ids := append([]int(nil), design.MainQuestIDs...)
			path := filepath.Join(t.TempDir(), "state.db")
			seed := &player.Starter{Version: "2.35.10", Characters: []player.Character{
				{InvenIndex: 77, ID: 10, Level: 1, HP: 7, CostumeID: 101, TalentLevel: 1},
				{InvenIndex: 78, ID: 20, Level: 1, HP: 7, CostumeID: 202, TalentLevel: 1},
				{InvenIndex: 79, ID: 130, Level: 1, HP: 7, CostumeID: 1301, TalentLevel: 1},
				{InvenIndex: 80, ID: 140, Level: 1, HP: 7, CostumeID: 1401, TalentLevel: 1},
				{InvenIndex: 81, ID: 350, Level: 1, HP: 7, CostumeID: 3501, TalentLevel: 1},
			}}
			chosen := []deck.DeckEntry{
				{CharacterInvenIndex: 81, CostumeInvenIndex: 0, Slot: 3},
				{CharacterInvenIndex: 79, CostumeInvenIndex: 11, Slot: 1},
				{CharacterInvenIndex: 77, CostumeInvenIndex: 3, Slot: 5},
				{CharacterInvenIndex: 80, CostumeInvenIndex: ^uint64(0), Slot: 2},
				{CharacterInvenIndex: 78, CostumeInvenIndex: 8, Slot: 4},
			}
			open := func() (*accountstate.Repository, *Service) {
				t.Helper()
				repo, err := accountstate.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = repo.Close() })
				s := testService()
				s.seed.PackID, s.startingPackID = pack, pack
				s.storyRoster, s.starter = roster, seed
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
				s.decks, err = deck.OpenStore(repo, deck.Seed{Version: "2.35.10", FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: 77}}})
				if err != nil {
					t.Fatal(err)
				}
				if len(s.decks.CurrentDeck()) == 0 {
					saveStoryTestDeck(t, s.decks, chosen)
				}
				s.quests = map[int]gamedata.QuestDesign{}
				for _, id := range ids {
					q := design.Quests[id]
					s.quests[id] = gamedata.QuestDesign{ID: id, Type: q.Type, NextQuestID: q.NextQuestID, PriorQuestID: q.PriorQuestID}
				}
				s.storyCatalog = &gamedata.StoryCatalog{Packs: map[int]gamedata.StoryPack{pack: {ID: pack, Quests: s.quests, MainQuestIDs: ids}}}
				s.packs = map[int]map[int]gamedata.QuestDesign{pack: s.quests}
				return repo, s
			}
			repo, s := open()
			unchanged := false
			for _, id := range ids {
				next := design.Quests[id].NextQuestID
				if next == 0 {
					continue
				}
				edges++
				assertStoryTestBattleParty(t, s, chosen)
				before, err := s.resolveStoryCharacters(pack, id)
				if err != nil {
					t.Fatal(err)
				}
				op, err := repo.BeginOperation()
				if err != nil {
					t.Fatal(err)
				}
				_, body, _, err := s.Handle("/QuestClear", selectionRequest(uint64(id), uint64(pack), 0))
				if err != nil {
					_ = op.Rollback()
					t.Fatal(err)
				}
				if err = op.Commit(); err != nil {
					t.Fatal(err)
				}
				after, err := s.resolveStoryCharacters(pack, next)
				if err != nil {
					t.Fatal(err)
				}
				known := map[uint64]player.Character{}
				for _, c := range before {
					known[c.InvenIndex] = c
				}
				var want []uint64
				for _, c := range after {
					old, exists := known[c.InvenIndex]
					if !exists || old.ID != c.ID || old.Level != c.Level || old.CostumeID != c.CostumeID || old.UseCostume != c.UseCostume {
						want = append(want, c.InvenIndex)
					}
				}
				echoed, got := storyTestClearParty(t, body)
				if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(echoed, chosen) {
					t.Fatalf("pack%d quest%d→%d joins=%v want=%v deck=%+v want=%+v", pack, id, next, got, want, echoed, chosen)
				}
				for _, index := range got {
					c, found := s.characters.Find(index)
					if !found || !player.IsStoryCharacter(c) {
						t.Fatalf("pack%d quest%d→%d fabricated owned join %+v", pack, id, next, c)
					}
				}
				assertStoryTestBattleParty(t, s, chosen)
				for _, c := range seed.Characters {
					current, found := s.characters.Find(c.InvenIndex)
					if !found || !reflect.DeepEqual(current, c) {
						t.Fatalf("pack%d quest%d→%d changed player state: got=%+v want=%+v", pack, id, next, current, c)
					}
				}
				unchanged = unchanged || len(want) == 0
				if len(want) != 0 {
					changedEdges++
				}
				if pack == 1 && id == 2 {
					if len(got) != 0 {
						t.Fatal("pack-1 gate quest repeated character acquisition")
					}
					if err = repo.Close(); err != nil {
						t.Fatal(err)
					}
					repo, s = open()
					assertStoryTestBattleParty(t, s, chosen)
					c, ok := s.characters.Find(77)
					if !ok || c.HP != 7 {
						t.Fatal("quest transition healed or replaced existing character")
					}
				}
			}
			if !unchanged {
				t.Fatal("authored regression did not exercise stable party transitions")
			}
			beforeReopen := s.characters.RawAll()
			if err := repo.Close(); err != nil {
				t.Fatal(err)
			}
			_, s = open()
			assertStoryTestBattleParty(t, s, chosen)
			if !reflect.DeepEqual(s.characters.RawAll(), beforeReopen) {
				t.Fatal("SQLite reopen changed player or temporary character state")
			}
		})
	}
	if edges == 0 || changedEdges == 0 {
		t.Fatalf("main-quest regression omitted edges or temporary joins: edges=%d changes=%d", edges, changedEdges)
	}
	t.Logf("validated %d packs, %d main-quest edges, and %d temporary join changes", len(packs), edges, changedEdges)
}

// Source: seed reward handling is server policy. Inventory acquisition must
// preserve the user's five saved battle choices, including cell 0 and -1.
func TestQuestUnlockRewardKeepsSavedBattleParty(t *testing.T) {
	s := testService()
	s.seed.BattleUnlockQuestID = 1
	s.seed.RewardCharacter = player.Character{InvenIndex: 900, ID: 90, Level: 1, HP: 100}
	s.seed.RewardCostume = player.Costume{InvenIndex: 901, ID: 901}
	s.seed.StoryCharacters = []player.Character{{InvenIndex: 902, ID: 10090, Level: 1, HP: 100}}
	var err error
	s.decks, err = deck.NewStore(deck.Seed{Version: "2.35.10", FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: 105}}})
	if err != nil {
		t.Fatal(err)
	}
	saved := []deck.DeckEntry{
		{CharacterInvenIndex: 105, CostumeInvenIndex: 0, Slot: 1},
		{CharacterInvenIndex: 104, CostumeInvenIndex: 8, Slot: 2},
		{CharacterInvenIndex: 103, CostumeInvenIndex: 11, Slot: 3},
		{CharacterInvenIndex: 102, CostumeInvenIndex: 3, Slot: 4},
		{CharacterInvenIndex: 101, CostumeInvenIndex: ^uint64(0), Slot: 5},
	}
	saveStoryTestDeck(t, s.decks, saved)
	_, response, handled, err := s.Handle("/QuestClear", selectionRequest(1, 21, 0))
	if err != nil || !handled {
		t.Fatalf("unlock clear handled=%v err=%v", handled, err)
	}
	echoed, joins := storyTestClearParty(t, response)
	if !reflect.DeepEqual(echoed, saved) || !reflect.DeepEqual(s.decks.CurrentDeck(), saved) || len(joins) != 0 {
		t.Fatalf("unlock reward replaced deck or fabricated joins: got=%+v want=%+v joins=%v", echoed, saved, joins)
	}
	rewardFound := false
	if err := wire.Walk(response, func(f wire.Field) error {
		if f.Number != 1 {
			return nil
		}
		return wire.Walk(f.Value, func(reward wire.Field) error {
			if reward.Number == 2 {
				index, _, err := wire.Varint(reward.Value, 1)
				rewardFound = rewardFound || index == s.seed.RewardCharacter.InvenIndex
				return err
			}
			return nil
		})
	}); err != nil || !rewardFound {
		t.Fatalf("saved party policy lost unlock reward: found=%v err=%v", rewardFound, err)
	}
}

func saveStoryTestDeck(t *testing.T, store *deck.Store, entries []deck.DeckEntry) {
	t.Helper()
	request := wire.AppendVarint(nil, 1, 1)
	for _, entry := range entries {
		data := wire.AppendVarint(nil, 1, entry.CharacterInvenIndex)
		data = wire.AppendVarint(data, 2, entry.CostumeInvenIndex)
		data = wire.AppendVarint(data, 3, entry.Slot)
		request = wire.AppendBytes(request, 2, data)
	}
	if _, _, handled, err := store.Handle("/DeckSave", request); err != nil || !handled {
		t.Fatalf("save test player deck handled=%v err=%v", handled, err)
	}
}

func decodeStoryTestDeck(t *testing.T, wires [][]byte) []deck.DeckEntry {
	t.Helper()
	var out []deck.DeckEntry
	for _, data := range wires {
		index, present, err := wire.Varint(data, 1)
		if err != nil || !present {
			t.Fatalf("deck character absent: %v", err)
		}
		// Proto/Net/DeckDBInfo.position_ defaults to 0 when field 2 is omitted.
		position, _, err := wire.Varint(data, 2)
		if err != nil {
			t.Fatalf("deck position: %v", err)
		}
		slot, present, err := wire.Varint(data, 3)
		if err != nil || !present {
			t.Fatalf("deck slot absent: %v", err)
		}
		out = append(out, deck.DeckEntry{CharacterInvenIndex: index, CostumeInvenIndex: position, Slot: slot})
	}
	return out
}

func storyTestClearParty(t *testing.T, response []byte) ([]deck.DeckEntry, []uint64) {
	t.Helper()
	var decks [][]byte
	var joins []uint64
	if err := wire.Walk(response, func(f wire.Field) error {
		if f.Number == 4 {
			decks = append(decks, f.Value)
		}
		if f.Number == 5 {
			index, present, err := wire.Varint(f.Value, 1)
			if err != nil || !present {
				t.Fatalf("join character absent: %v", err)
			}
			joins = append(joins, index)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return decodeStoryTestDeck(t, decks), joins
}

func assertStoryTestBattleParty(t *testing.T, s *Service, saved []deck.DeckEntry) {
	t.Helper()
	party, err := s.currentBattleParty()
	if err != nil || len(party) != len(saved) {
		t.Fatalf("player battle party=%+v want=%+v err=%v", party, saved, err)
	}
	for i, c := range party {
		if c.InvenIndex != saved[i].CharacterInvenIndex || player.IsStoryCharacter(c) {
			t.Fatalf("displaced player %d: %+v want=%+v", i, c, saved[i])
		}
	}
	if !reflect.DeepEqual(s.decks.CurrentDeck(), saved) {
		t.Fatalf("rewrote saved positions or order: got=%+v want=%+v", s.decks.CurrentDeck(), saved)
	}
}
