package monsterhunt

import (
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
)

func TestPresetPurchaseRollsBackCurrencyAndSlotTogether(t *testing.T) {
	s, _ := installed(t)
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
	wallet, err := player.OpenWallet(repo, player.Currency{Gold: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	s.storage = repo
	s.wallet = wallet
	before := s.state.Slots
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	req := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1)
	if _, _, _, err = s.HandleSession("/MonsterHuntPresetSlotAdd", req, "session-A"); err != nil {
		_ = op.Rollback()
		t.Fatal(err)
	}
	_ = op.Rollback()
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	if raw, err := reopened.Load("monsterhunt"); err != nil || raw != nil {
		t.Fatalf("slot survived rollback: %s %v", raw, err)
	}
	wallet, err = player.OpenWallet(reopened, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 100000 {
		t.Fatalf("gold survived rollback: %+v", wallet.Snapshot())
	}
	if s.state.Slots != before+1 || repo.Check() == nil {
		t.Fatal("dirty domain memory was not fenced")
	}
}
