package world

import (
	"bd2server/internal/server/accountstate"
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
// pack. The source-backed contract is PackInGameInfo replacing both client
// quest lists (CommonPacket.SendPackInGameInfoRequest), not an official capture.
func TestCommissionPackEntryRestoresSQLiteChainAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	design := &gamedata.TodayQuestCatalog{Limit: 3, PostCount: 3, AchievementScore: 7,
		Reset: gamedata.FieldResetSchedule{WeeklyDay: time.Monday, DailyReset: 9 * time.Hour},
		Quests: map[int]gamedata.TodayQuest{
			101: {ID: 101, PackID: 21, NextID: 102, ConditionType: 19, ConditionCount: 1},
			102: {ID: 102, PackID: 21, PriorID: 101, ConditionType: 2, ConditionCount: 2, MagicValues: []uint64{71, 72}, GiveItemIDs: []uint64{100001}, Rewards: []gamedata.Reward{{Type: 4, Count: 10}}},
			201: {ID: 201, PackID: 22, ConditionType: 19, ConditionCount: 1},
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
		return b
	}
	assertEntry := func(s *Service, pack int, active map[int][]uint64, cleared []int) {
		t.Helper()
		request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, uint64(pack))
		_, body, handled, err := s.Handle("/PackInGameInfo", request)
		if !handled || err != nil {
			t.Fatalf("pack entry: handled=%v %v", handled, err)
		}
		got := map[int][]uint64{}
		var clears []int
		if err := wire.Walk(body, func(f wire.Field) error {
			if f.Number == 2 {
				id, _, _ := wire.Varint(f.Value, 1)
				if _, exists := design.Quests[int(id)]; exists {
					got[int(id)] = nil
					return wire.Walk(f.Value, func(q wire.Field) error {
						if q.Number == 3 {
							n, _ := binary.Uvarint(q.Value)
							got[int(id)] = append(got[int(id)], n)
						}
						return nil
					})
				}
			}
			if f.Number == 3 {
				for b := f.Value; len(b) > 0; {
					id, n := binary.Uvarint(b)
					if n <= 0 {
						t.Fatal("invalid cleared IDs")
					}
					if _, exists := design.Quests[int(id)]; exists {
						clears = append(clears, int(id))
					}
					b = b[n:]
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, active) || !reflect.DeepEqual(clears, cleared) {
			t.Fatalf("pack %d active=%v cleared=%v", pack, got, clears)
		}
	}
	repo, s, wallet, inv := open()
	call(s, "/QuestAccept", 101, 21)
	call(s, "/QuestAccept", 201, 22)
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
	call(s, "/QuestClear", 102, 21)
	assertEntry(s, 21, map[int][]uint64{}, []int{101, 102})
	if wallet.Snapshot().Gold != 10 {
		t.Fatal("reward replay changed balance")
	}
	call(s, "/QuestGiveUp", 201, 22)
	assertEntry(s, 22, map[int][]uint64{}, nil)
}
