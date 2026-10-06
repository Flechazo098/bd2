package events

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"path/filepath"
	"testing"
)

func TestAdditionalRewardCurrenciesAndRankChangeItemPersistAndReplay(t *testing.T) {
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
	eco, items, _ := economyFixture(t, repo, &economyGraph{})
	eco.AttachOwnedItemDesign(map[uint64]map[uint64]bool{69: {1: true}})
	rewards := []gamedata.Reward{{Type: 38, Count: 1}, {Type: 39, Count: 20}, {Type: 40, Count: 30}, {Type: 54, Count: 40}, {Type: 70, Count: 50}, {Type: 69, ID: 1, Count: 2}}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	first, err := eco.Apply("new reward types", nil, rewards)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	assertBalances := func(e *Economy) {
		t.Helper()
		got, err := e.AdditionalCurrencies()
		if err != nil {
			t.Fatal(err)
		}
		for field, want := range map[int]uint64{42: 1, 44: 20, 45: 30, 61: 40, 71: 50} {
			if got[field] != want {
				t.Fatalf("UserDB field%d=%d want%d", field, got[field], want)
			}
		}
	}
	assertBalances(eco)
	if len(items.All()) != 1 || items.All()[0].Type != 69 {
		t.Fatal("currencies stored as inventory", items.All())
	}
	types := map[uint64]bool{}
	_ = wire.Walk(first, func(f wire.Field) error {
		if f.Number == 1 {
			typ, _, _ := wire.Varint(f.Value, 3)
			types[typ] = true
		}
		return nil
	})
	for _, reward := range rewards {
		if !types[reward.Type] {
			t.Fatal("native currency/item receipt missing", reward.Type)
		}
	}
	eco, items, _ = economyFixture(t, repo, &economyGraph{})
	eco.AttachOwnedItemDesign(map[uint64]map[uint64]bool{69: {1: true}})
	replay, err := eco.Apply("new reward types", nil, rewards)
	if err != nil || !bytes.Equal(first, replay) || len(items.All()) != 1 {
		t.Fatal("restart replay duplicated", err)
	}
	assertBalances(eco)
	if _, err = eco.Apply("invalid item", nil, []gamedata.Reward{{Type: 54, Count: 100}, {Type: 69, ID: 999, Count: 1}}); err == nil {
		t.Fatal("unknown rank change design accepted")
	}
	assertBalances(eco)
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = eco.Apply("rollback", nil, []gamedata.Reward{{Type: 39, Count: 100}, {Type: 70, Count: 100}}); err != nil {
		t.Fatal(err)
	}
	_ = op.Rollback()
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	eco, _, _ = economyFixture(t, repo, &economyGraph{})
	assertBalances(eco)
}
