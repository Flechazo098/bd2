package player

import (
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
)

func TestWalletGrantPersistsAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallet.json")
	wallet, err := OpenWallet(testStore(path), Currency{Gold: 100})
	if err != nil {
		t.Fatal(err)
	}
	rewards := []gamedata.Reward{{Type: 4, Count: 1500}, {Type: 3, Count: 70}, {Type: 12, Count: 45}, {Type: 25, ID: 210, Count: 1}}
	if _, err := wallet.GrantQuestOnce("quest:1", rewards); err != nil {
		t.Fatal(err)
	}
	wallet, err = OpenWallet(testStore(path), Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.GrantQuestOnce("quest:1", rewards); err != nil {
		t.Fatal(err)
	}
	got := wallet.Snapshot()
	if got.Gold != 1600 || got.FreeJewelry != 70 || got.Catalyst != 45 {
		t.Fatalf("wallet=%+v", got)
	}
}
