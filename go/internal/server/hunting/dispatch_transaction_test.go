package hunting

import (
	"errors"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type failedDispatchStore struct{ stateio.Store }

func (s failedDispatchStore) Save(name string, payload []byte) error {
	if name == "huntdispatch" {
		return errors.New("injected dispatch receipt write failure")
	}
	return s.Store.Save(name, payload)
}

func TestDispatchReceiptFailureRollsBackAPAndRewards(t *testing.T) {
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
	inv, err := player.OpenInventory(repo, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(repo, player.Currency{Gold: 12})
	if err != nil {
		t.Fatal(err)
	}
	if err = inv.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err = wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(failedDispatchStore{repo}, "", "", inv, wallet, func() (int, error) { return 1, nil }, 10, 4)
	if err != nil {
		t.Fatal(err)
	}
	s.state.Packs["1"] = packState{Highest: 1}
	s.dispatchLoad = func(group, id uint64) (*gamedata.DispatchDesign, error) {
		return &gamedata.DispatchDesign{Group: group, ID: id, Pack: 1, GroundID: 1, AP: 2, Rewards: []gamedata.BattleReward{{Type: 4, Count: 100}, {Type: 8, ID: 987, Count: 3}}}, nil
	}
	operation, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	req := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1), 3, 1), 4, 2)
	if _, _, _, err = s.HandleSession("/HuntDispatch", req, "session-A"); err == nil {
		_ = operation.Rollback()
		t.Fatal("receipt failure accepted")
	}
	_ = operation.Rollback()
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
	for _, name := range []string{"hunting", "huntdispatch"} {
		raw, err := reopened.Load(name)
		if err != nil || raw != nil {
			t.Fatalf("%s survived rollback: %s %v", name, raw, err)
		}
	}
	inv, err = player.OpenInventory(reopened, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err = player.OpenWallet(reopened, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.All()) != 0 || wallet.Snapshot().Gold != 12 {
		t.Fatalf("dispatch rewards survived rollback: %+v %+v", inv.All(), wallet.Snapshot())
	}
}
