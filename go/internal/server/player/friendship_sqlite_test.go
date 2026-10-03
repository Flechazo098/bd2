package player

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
)

type friendshipFailStore struct {
	stateio.AtomicEntryStore
	fail bool
}

func (s *friendshipFailStore) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if s.fail && domain == "collection" {
		return errors.New("injected collection write failure")
	}
	return s.AtomicEntryStore.SaveWithEntries(domain, core, changes)
}

func sqliteFriendshipService(t *testing.T, store stateio.Store, template *FriendshipService) (*FriendshipService, *Inventory, *Wallet) {
	t.Helper()
	collection, err := OpenCollectionStore(store, []Costume{{InvenIndex: 1, ID: 100}, {InvenIndex: 2, ID: 200}, {InvenIndex: 3, ID: 300}, {InvenIndex: 4, ID: 400}})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(store, Currency{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewFriendshipService(template.design, template.awake, template.potential, collection, inventory, wallet)
	if err != nil {
		t.Fatal(err)
	}
	s.BeginSession("durable-session")
	return s, inventory, wallet
}

func TestFriendshipSQLiteAtomicFailureRestartAndExactRetry(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit-replay", true: "rollback-retry"}[rollback], func(t *testing.T) {
			template := newFriendshipHarness(t).service
			path := filepath.Join(t.TempDir(), "state.db")
			repo, err := accountstate.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			store := &friendshipFailStore{AtomicEntryStore: repo}
			s, inventory, wallet := sqliteFriendshipService(t, store, template)
			items, err := inventory.GrantOnce("seed", []gamedata.BattleReward{{Type: 8, ID: 7, Count: 3}})
			if err != nil {
				t.Fatal(err)
			}
			item := items[0]
			item.Count = 1
			request := giftRequest(1, 10, item)
			op, err := repo.BeginOperation()
			if err != nil {
				t.Fatal(err)
			}
			store.fail = rollback
			_, body, _, handleErr := s.Handle("/FriendshipGift", request)
			if rollback {
				if handleErr == nil {
					t.Fatal("injected failure was ignored")
				}
				if err := op.Rollback(); err == nil {
					t.Fatal("dirty rollback must fence writer until restart")
				}
			} else {
				if handleErr != nil {
					t.Fatal(handleErr)
				}
				if err := op.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.Close(); err != nil {
				t.Fatal(err)
			}
			repo, err = accountstate.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()
			s, inventory, wallet = sqliteFriendshipService(t, repo, template)
			if rollback {
				if wallet.Snapshot().FreeJewelry != 0 || len(s.collection.FriendshipEntries()) != 0 {
					t.Fatal("failed operation left committed rewards or progress")
				}
				if err := inventory.CanConsume(items); err != nil {
					t.Fatal("failed gift consumed committed inventory")
				}
			}
			op, err = repo.BeginOperation()
			if err != nil {
				t.Fatal(err)
			}
			_, replay, _, err := s.Handle("/FriendshipGift", request)
			if err != nil {
				t.Fatal(err)
			}
			if err := op.Commit(); err != nil {
				t.Fatal(err)
			}
			if !rollback && !bytes.Equal(body, replay) {
				t.Fatal("restart response changed")
			}
			if wallet.Snapshot().FreeJewelry != 7 {
				t.Fatal("reward was not applied exactly once")
			}
			if err := inventory.CanConsume([]Item{{InvenIndex: item.InvenIndex, ID: 7, Type: 8, Count: 2}}); err != nil {
				t.Fatal(err)
			}
			if err := inventory.CanConsume(items); err == nil {
				t.Fatal("request was not charged exactly once")
			}
		})
	}
}
