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

// Current client QuestPacket feeds every QuestClear.CharInfo into the join UI.
// Exercise the real authored formations through quest clear and SQLite reopen,
// including the pack-1 gate quest that used to reopen that UI unnecessarily.
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
				var chosen []deck.DeckEntry
				for i, c := range seed.Characters {
					chosen = append(chosen, deck.DeckEntry{CharacterInvenIndex: c.InvenIndex, CostumeInvenIndex: uint64(i), Slot: uint64(i + 1)})
				}
				s.decks, err = deck.OpenStore(repo, deck.Seed{Version: "2.35.10", FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: 77}}})
				if err != nil {
					t.Fatal(err)
				}
				if len(s.decks.CurrentDeck()) == 0 {
					if err = s.decks.SetStoryParty(chosen); err != nil {
						t.Fatal(err)
					}
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
				before, err := s.ResolveStoryParty(pack, id)
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
				after, err := s.ResolveStoryParty(pack, next)
				if err != nil {
					t.Fatal(err)
				}
				known := map[uint64]player.Character{}
				for _, c := range before {
					known[c.InvenIndex] = c
				}
				var want, got []uint64
				for _, c := range after {
					old, exists := known[c.InvenIndex]
					if !exists || old.ID != c.ID || old.Level != c.Level || old.CostumeID != c.CostumeID || old.UseCostume != c.UseCostume {
						want = append(want, c.InvenIndex)
					}
				}
				decks := 0
				if err = wire.Walk(body, func(f wire.Field) error {
					if f.Number == 5 {
						index, _, err := wire.Varint(f.Value, 1)
						got = append(got, index)
						return err
					}
					if f.Number == 4 {
						decks++
					}
					return nil
				}); err != nil || !reflect.DeepEqual(got, want) || len(after) > 0 && decks != len(after) || decks > 5 {
					t.Fatalf("pack%d quest%d→%d joins=%v want=%v formation=%d/%d err=%v", pack, id, next, got, want, decks, len(after), err)
				}
				unchanged = unchanged || len(before) > 0 && len(want) == 0
				if pack == 1 && id == 2 {
					if len(got) != 0 {
						t.Fatal("pack-1 gate quest repeated character acquisition")
					}
					if err = repo.Close(); err != nil {
						t.Fatal(err)
					}
					repo, s = open()
					c, ok := s.characters.Find(77)
					if !ok || c.HP != 7 {
						t.Fatal("quest transition healed or replaced existing character")
					}
				}
			}
			if !unchanged {
				t.Fatal("authored regression did not exercise stable party transitions")
			}
		})
	}
}
