package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/deck"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/todayquest"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type commissionEconomy struct{ wallet *player.Wallet }

func (e commissionEconomy) Apply(id string, costs, rewards []gamedata.Reward) ([]byte, error) {
	_, err := e.wallet.GrantQuestOnce(id, rewards)
	return nil, err
}

// Synthetic IDs describe a two-node collection commission and an independent
// pack. CommonPacket parses PackInGameInfo, requests TodayQuestInfo, then calls
// PackManager.Enter from the waypoint callback. Those last two callbacks can
// arrive in either order; only TodayQuestInfo restores commissions.
func TestCommissionPackEntryRestoresSQLiteChainAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	design := &gamedata.TodayQuestCatalog{Limit: 3, PostCount: 3, AchievementScore: 7,
		Reset: gamedata.FieldResetSchedule{WeeklyDay: time.Monday, DailyReset: 9 * time.Hour},
		Quests: map[int]gamedata.TodayQuest{
			101: {ID: 101, PackID: 21, NextID: 102, ConditionType: 19, ConditionCount: 1},
			102: {ID: 102, PackID: 21, PriorID: 101, ConditionType: 2, ConditionCount: 2, MagicValues: []uint64{71, 72}, GiveItemIDs: []uint64{100001}, Rewards: []gamedata.Reward{{Type: 4, Count: 10}}},
			201: {ID: 201, PackID: 22, ConditionType: 19, ConditionCount: 1},
			301: {ID: 301, PackID: 21, ConditionType: 19, ConditionCount: 1},
		}}
	open := func() (*accountstate.Repository, *Service, *player.Wallet, *player.Inventory) {
		t.Helper()
		repo, err := accountstate.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Close() })
		s := testService()
		s.storyCatalog.Packs[22] = gamedata.StoryPack{ID: 22, Quests: map[int]gamedata.QuestDesign{}}
		s.state, err = progress.OpenStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		s.decks, err = deck.OpenStore(repo, deck.Seed{Version: "2.35.10", FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: 901}}})
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := player.OpenWallet(repo, player.Currency{})
		if err != nil {
			t.Fatal(err)
		}
		inv, err := player.OpenInventory(repo, &player.Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		store := stateio.EntrySnapshotStore{Entries: repo, Domain: "missions", Bucket: "gameplay"}
		commissions, err := todayquest.Open(store, design, commissionEconomy{wallet}, inv, func(int) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		commissions.CompleteAchievement = func(id string) error { return store.Save("commission_test_achievement", []byte(id)) }
		if err := s.AttachTodayQuests(commissions); err != nil {
			t.Fatal(err)
		}
		return repo, s, wallet, inv
	}
	call := func(s *Service, path string, id, pack int, values ...uint64) []byte {
		t.Helper()
		req := selectionRequest(uint64(id), uint64(pack), 0)
		if len(values) > 0 {
			// QuestUpdate uses field 4 as repeated int32, not difficulty.
			req = wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, uint64(id)), 3, uint64(pack))
			for _, value := range values {
				req = wire.AppendVarint(req, 4, value)
			}
		}
		_, b, handled, err := s.Handle(path, req)
		if !handled || err != nil {
			t.Fatalf("%s: handled=%v %v", path, handled, err)
		}
		// QuestAccept, QuestClear and QuestGiveUp all call client RefreshDeck
		// with a non-null repeated list: the response must contain the complete
		// current battle deck, even for a replayed commission reward receipt.
		deckField := map[string]int{"/QuestAccept": 3, "/QuestClear": 4, "/QuestGiveUp": 2}[path]
		if deckField != 0 {
			var entries [][]byte
			if err := wire.Walk(b, func(f wire.Field) error {
				if f.Number == deckField {
					entries = append(entries, f.Value)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if got := decodeStoryTestDeck(t, entries); !reflect.DeepEqual(got, s.decks.CurrentDeck()) {
				t.Fatalf("%s replaced saved formation: got=%+v want=%+v", path, got, s.decks.CurrentDeck())
			}
		}
		return b
	}
	assertEntry := func(s *Service, pack int, active map[int][]uint64, cleared []int) {
		t.Helper()
		request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, uint64(pack))
		_, body, handled, err := s.Handle("/PackInGameInfo", request)
		if !handled || err != nil {
			t.Fatalf("pack entry: handled=%v %v", handled, err)
		}
		var packQuests []int
		var packClears []int
		// The pack response is parsed before TodayQuestInfo is requested. Its
		// regular task list is applied later, when the waypoint request finishes.
		if err := wire.Walk(body, func(f wire.Field) error {
			if f.Number == 2 {
				id, _, _ := wire.Varint(f.Value, 1)
				if _, exists := design.Quests[int(id)]; exists {
					t.Fatalf("commission %d duplicated into PackInGameInfo", id)
				}
				packQuests = append(packQuests, int(id))
			}
			if f.Number == 3 {
				for b := f.Value; len(b) > 0; {
					id, n := binary.Uvarint(b)
					if n <= 0 {
						t.Fatal("invalid cleared IDs")
					}
					if _, exists := design.Quests[int(id)]; exists {
						t.Fatalf("commission %d restored through pack completion list", id)
					}
					packClears = append(packClears, int(id))
					b = b[n:]
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if pack == 21 && !reflect.DeepEqual(packQuests, []int{1}) {
			t.Fatalf("pack entry lost ordinary main quest: %v", packQuests)
		}
		today := call(s, "/TodayQuestInfo", 0, 0)
		got := map[int][]uint64{}
		var clears []int
		var currentPackToday []int
		if err := wire.Walk(today, func(f wire.Field) error {
			if f.Number == 1 {
				id, _, _ := wire.Varint(f.Value, 1)
				qpack, _, _ := wire.Varint(f.Value, 6)
				if int(qpack) == pack {
					if _, duplicate := got[int(id)]; duplicate {
						t.Fatalf("TodayQuestInfo repeated commission %d", id)
					}
					got[int(id)] = nil
					currentPackToday = append(currentPackToday, int(id))
					return wire.Walk(f.Value, func(q wire.Field) error {
						if q.Number == 3 {
							n, _ := binary.Uvarint(q.Value)
							got[int(id)] = append(got[int(id)], n)
						}
						return nil
					})
				}
			}
			if f.Number == 2 {
				id, _ := binary.Uvarint(f.Value)
				if design.Quests[int(id)].PackID == pack {
					clears = append(clears, int(id))
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, active) || !reflect.DeepEqual(clears, cleared) {
			t.Fatalf("pack %d TodayQuestInfo active=%v cleared=%v", pack, got, clears)
		}
		for _, todayFirst := range []bool{true, false} {
			var clientQuests []int
			// The pack completion snapshot was assigned before either of
			// these callbacks; TodayQuestInfo subsequently merges its IDs.
			clientClears := append([]int(nil), packClears...)
			addToday := func() {
				var ordinary []int
				for _, id := range clientQuests {
					if _, today := design.Quests[id]; !today {
						ordinary = append(ordinary, id)
					}
				}
				clientQuests = append(ordinary, currentPackToday...)
				for _, id := range clears {
					present := false
					for _, existing := range clientClears {
						present = present || existing == id
					}
					if !present {
						clientClears = append(clientClears, id)
					}
				}
			}
			if todayFirst {
				addToday()
				clientQuests = append(clientQuests, packQuests...)
			} else {
				clientQuests = append(clientQuests, packQuests...)
				addToday()
			}
			seen := map[int]bool{}
			for _, id := range clientQuests {
				if seen[id] {
					t.Fatalf("HUD duplicate quest %d when todayFirst=%v", id, todayFirst)
				}
				seen[id] = true
			}
			if len(seen) != len(packQuests)+len(active) {
				t.Fatalf("entry callbacks lost tasks when todayFirst=%v: %v", todayFirst, clientQuests)
			}
			var completedToday []int
			for _, id := range clientClears {
				if _, today := design.Quests[id]; today {
					completedToday = append(completedToday, id)
				}
			}
			if !reflect.DeepEqual(completedToday, cleared) {
				t.Fatalf("entry callbacks lost completed commissions when todayFirst=%v: %v", todayFirst, completedToday)
			}
		}
	}
	repo, s, wallet, inv := open()
	savedDeck := []deck.DeckEntry{
		{CharacterInvenIndex: 901, CostumeInvenIndex: 0, Slot: 1},
		{CharacterInvenIndex: 905, CostumeInvenIndex: 8, Slot: 2},
		{CharacterInvenIndex: 903, CostumeInvenIndex: 11, Slot: 3},
		{CharacterInvenIndex: 904, CostumeInvenIndex: 3, Slot: 4},
		{CharacterInvenIndex: 902, CostumeInvenIndex: ^uint64(0), Slot: 5},
	}
	saveStoryTestDeck(t, s.decks, savedDeck)
	call(s, "/QuestAccept", 101, 21)
	call(s, "/QuestAccept", 201, 22)
	call(s, "/QuestAccept", 301, 21)
	call(s, "/QuestAccept", 101, 21)
	call(s, "/QuestGiveUp", 301, 21)
	if !reflect.DeepEqual(s.decks.CurrentDeck(), savedDeck) {
		t.Fatal("commission accepts or abandonment changed saved formation")
	}
	call(s, "/QuestUpdate", 101, 21, 1)
	call(s, "/QuestClear", 101, 21)
	call(s, "/QuestUpdate", 102, 21, 71)
	if len(inv.All()) != 1 {
		t.Fatal("continuation item missing")
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, s, wallet, inv = open()
	if !reflect.DeepEqual(s.decks.CurrentDeck(), savedDeck) {
		t.Fatal("SQLite reopen lost selected formation")
	}
	assertEntry(s, 21, map[int][]uint64{102: {71}}, []int{101})
	assertEntry(s, 22, map[int][]uint64{201: nil}, nil)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	call(s, "/QuestUpdate", 102, 21, 72)
	call(s, "/QuestClear", 102, 21)
	if err := op.Rollback(); !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatalf("dirty rollback must require domain reopen: %v", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, s, wallet, inv = open()
	assertEntry(s, 21, map[int][]uint64{102: {71}}, []int{101})
	if wallet.Snapshot().Gold != 0 {
		t.Fatal("rolled-back reward persisted")
	}
	if len(inv.All()) != 1 {
		t.Fatal("continuation item duplicated on restart")
	}
	call(s, "/QuestUpdate", 102, 21, 72)
	call(s, "/QuestClear", 102, 21)
	// A retry is an idempotent reward operation, but its deck projection is a
	// live snapshot. Persist a different formation and reopen before replay.
	savedDeck[0], savedDeck[4] = savedDeck[4], savedDeck[0]
	savedDeck[0].Slot, savedDeck[4].Slot = 1, 5
	saveStoryTestDeck(t, s.decks, savedDeck)
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, s, wallet, inv = open()
	call(s, "/QuestClear", 102, 21)
	assertEntry(s, 21, map[int][]uint64{}, []int{101, 102})
	if wallet.Snapshot().Gold != 10 {
		t.Fatal("reward replay changed balance")
	}
	if !reflect.DeepEqual(s.decks.CurrentDeck(), savedDeck) {
		t.Fatal("replayed commission clear restored stale formation")
	}
	call(s, "/QuestGiveUp", 201, 22)
	assertEntry(s, 22, map[int][]uint64{}, nil)
}
