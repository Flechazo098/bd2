package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"errors"
	"path/filepath"
	"testing"
)

func TestPaidPackUsesDesignCostAndCurrenciesOnce(t *testing.T) {
	storage := stateio.NewMemory()
	s := testService()
	var err error
	s.inventory, err = player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	s.collection, err = player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(storage, player.Currency{FreeJewelry: 101})
	if err != nil {
		t.Fatal(err)
	}
	s.storyCatalog.Packs[709] = gamedata.StoryPack{ID: 709, BuyType: 3, BuyPrice: 37, BuyRewards: []gamedata.Reward{{Type: 2, Count: 8}, {Type: 20, Count: 11}}}
	for range 2 {
		if _, err := s.purchaseStoryPack(709, false); err != nil {
			t.Fatal(err)
		}
	}
	got := s.wallet.Snapshot()
	if got.FreeJewelry != 64 || got.Jewelry != 8 || got.Mileage != 11 {
		t.Fatalf("currency=%+v", got)
	}
	s.storyCatalog.Packs[710] = gamedata.StoryPack{ID: 710, BuyType: 3, BuyPrice: 37, BuyRewards: []gamedata.Reward{{Type: 8, ID: 4, Count: 1}}}
	if _, err := s.purchaseStoryPack(710, false); err == nil {
		t.Fatal("invalid reward accepted")
	}
	if s.wallet.Snapshot().FreeJewelry != 64 {
		t.Fatal("invalid reward charged")
	}
}

type purchaseFailStore struct {
	*accountstate.Repository
	fail bool
}

func (s *purchaseFailStore) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if s.fail && domain == "collection" {
		return errors.New("forced purchase marker failure")
	}
	return s.Repository.SaveWithEntries(domain, core, changes)
}

func TestPackPurchaseFailureRollsBackSQLiteCurrencyTicketAndMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	storage := &purchaseFailStore{Repository: repo}
	s := testService()
	s.seed.PackID = 709
	s.inventory, err = player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	s.wallet, err = player.OpenWallet(storage, player.Currency{Catalyst: 5})
	if err != nil {
		t.Fatal(err)
	}
	s.collection, err = player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.wallet.GrantQuestOnce("test:baseline", nil); err != nil {
		t.Fatal(err)
	}
	s.storyCatalog = &gamedata.StoryCatalog{Packs: map[int]gamedata.StoryPack{709: {ID: 709, BuyRewards: []gamedata.Reward{{Type: 12, Count: 17}, {Type: 19, ID: 88, Count: 1}}}}}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	storage.fail = true
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 709)
	if _, _, _, err := s.Handle("/PackBuy", request); err == nil {
		t.Fatal("forced collection failure returned success")
	}
	if err := op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	storage.Repository = repo
	storage.fail = false
	// Requests reconstruct domains following a rollback; in-memory snapshots
	// that participated in the aborted operation must not be reused.
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.CatalystBalance() != 5 || wallet.WasGranted("pack-purchase:709:currency") || len(inventory.All()) != 0 {
		t.Fatal("aborted purchase persisted partial awards")
	}
	if _, found := collection.Grant("pack-purchase:709"); found {
		t.Fatal("aborted purchase persisted marker")
	}
	s.wallet, s.inventory, s.collection = wallet, inventory, collection
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/PackBuy", request); err != nil {
		if rollbackErr := op.Rollback(); rollbackErr != nil {
			t.Error(rollbackErr)
		}
		t.Fatal(err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	if wallet.CatalystBalance() != 22 || len(inventory.All()) != 1 {
		t.Fatal("retried purchase did not grant exactly once")
	}
}
