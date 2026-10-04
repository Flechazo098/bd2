package hunting

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
)

func TestHuntingSettlementRollsBackAPWalletInventoryAndProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	inv, err := player.OpenInventory(repo, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(repo, player.Currency{Gold: 12})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	load := func(int) (*gamedata.HuntingPack, error) {
		return &gamedata.HuntingPack{NormalAP: 1, BossAP: 2,
			Grounds:  []gamedata.HuntingGround{{ID: 1, MapID: 13, BossID: 11}},
			Monsters: map[uint64]gamedata.HuntingMonster{11: {ID: 11, Type: 1, Decks: []uint64{21}, Rewards: map[uint64][]gamedata.BattleReward{21: {{Type: 4, Count: 100}, {Type: 8, ID: 987, Count: 3}}}}}}, nil
	}
	s, err := Open(repo, "", "", inv, wallet, func() (int, error) { return 1, nil }, 10, 4)
	if err != nil {
		t.Fatal(err)
	}
	s.load = load
	enter := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1), 3, 1)
	if _, _, _, err := s.Handle("/HuntingGroundEnter", enter); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Load("hunting")
	if err != nil {
		t.Fatal(err)
	}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteBattle(1, BattleMode, 11, 21, "login-A:2"); err != nil {
		_ = op.Rollback()
		t.Fatal(err)
	}
	// Model a later domain failure in the same request/batch. A dirty rollback
	// fences in-memory domains, requiring restart before they can serve again.
	_ = op.Rollback()
	if repo.Check() == nil {
		t.Fatal("dirty rollback did not fence stale domains")
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.Load("hunting")
	if err != nil || string(after) != string(before) {
		t.Fatalf("hunting progress survived rollback: %s %v", after, err)
	}
	inv, err = player.OpenInventory(reopened, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err = player.OpenWallet(reopened, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	var persisted snapshot
	if err := json.Unmarshal(after, &persisted); err != nil {
		t.Fatal(err)
	}
	free, bonus := persisted.Free, persisted.Bonus
	if err != nil || free != 10 || bonus != 4 || wallet.Snapshot().Gold != 12 || len(inv.All()) != 0 {
		t.Fatalf("settlement survived rollback: AP=%d/%d wallet=%+v items=%+v err=%v", free, bonus, wallet.Snapshot(), inv.All(), err)
	}
}
