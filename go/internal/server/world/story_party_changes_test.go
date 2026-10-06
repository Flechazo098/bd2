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
	roster, err := gamedata.LoadStoryCharacterCatalog(root, "20260923193640", []int{1, 21}, 996000)
	if err != nil {
		t.Fatal(err)
	}
	for _, pack := range []int{1, 21} {
		t.Run(fmt.Sprintf("pack%d", pack), func(t *testing.T) {
			formations, err := gamedata.LoadQuestFormations(root, "20260923193640", pack)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int
			for id := range formations {
				ids = append(ids, id)
			}
			sort.Ints(ids)
			path := filepath.Join(t.TempDir(), "state.db")
			seed := &player.Starter{Version: "2.35.10", Characters: []player.Character{{InvenIndex: 77, ID: 10, Level: 1, HP: 7, CostumeID: 101}}}
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
				s.quests = map[int]gamedata.QuestDesign{}
				for _, id := range ids {
					s.quests[id] = gamedata.QuestDesign{ID: id}
				}
				attachTestStoryCatalog(s)
				return repo, s
			}
			repo, s := open()
			unchanged, joined := false, false
			for i, id := range ids[:len(ids)-1] {
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
				after, err := s.ResolveStoryParty(pack, ids[i+1])
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
				}); err != nil || !reflect.DeepEqual(got, want) || len(after) > 0 && decks != len(after) {
					t.Fatalf("pack%d quest%d→%d joins=%v want=%v formation=%d/%d err=%v", pack, id, ids[i+1], got, want, decks, len(after), err)
				}
				unchanged = unchanged || len(before) > 0 && len(want) == 0
				joined = joined || len(want) > 0
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
				if unchanged && (pack == 1 && id >= 3 || pack == 21 && joined) {
					break
				}
			}
			if !unchanged || pack == 21 && !joined {
				t.Fatal("authored regression did not exercise both stable and joining party transitions")
			}
		})
	}
}
