package npcinn

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func innTestService(t *testing.T, gold, level, state uint64) (*Service, *player.CharacterStore, *player.Wallet, stateio.Store) {
	t.Helper()
	store := stateio.NewMemory()
	return innTestServiceWithStore(t, store, gold, level, state)
}

func innTestServiceWithStore(t *testing.T, store stateio.Store, gold, level, state uint64) (*Service, *player.CharacterStore, *player.Wallet, stateio.Store) {
	t.Helper()
	inv, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	chars, err := player.OpenCharacterStore(store, []player.Character{{InvenIndex: 71, ID: 350, Level: 1, HP: 10}, {InvenIndex: 72, ID: 360, Level: 1, HP: 10}}, inv, "", "")
	if err != nil {
		t.Fatal(err)
	}
	chars.AttachMaxHealth(func(player.Character) (uint64, error) { return 100, nil })
	if err = chars.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(store, player.Currency{Gold: gold})
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	s, err := New(store, chars, wallet, func(pack, npc uint64) (gamedata.InnRule, uint64, error) {
		return gamedata.InnRule{NPCID: 7, MapID: 33, ItemCount: 2, Currency: 4, FreeSquadLevel: 20, GoodDiscount: 10}, state, nil
	}, func() (uint64, error) { return level, nil }, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	s.BeginSession("test")
	return s, chars, wallet, store
}

type innReceiptFailStore struct{ stateio.Store }

func (s innReceiptFailStore) Save(name string, b []byte) error {
	if name == "npcinn" {
		return errors.New("receipt write failed")
	}
	return s.Store.Save(name, b)
}

func TestInnReceiptFailureRollsBackGoldAndHealthTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _, _ := innTestServiceWithStore(t, repo, 10, 21, 1)
	s.store = innReceiptFailStore{repo}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/CharAllRevival", recoveryRequest(1, 71)); err == nil {
		t.Fatal("receipt failure ignored")
	}
	if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	repo.Close()
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	next, chars, wallet, _ := innTestServiceWithStore(t, repo, 0, 21, 1)
	hp, _ := chars.CurrentHealth(71)
	if hp != 10 || wallet.Snapshot().Gold != 10 {
		t.Fatal("failed inn request committed health or charge")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = next.Handle("/CharAllRevival", recoveryRequest(1, 71)); err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	hp, _ = chars.CurrentHealth(71)
	if hp != 100 || wallet.Snapshot().Gold != 5 {
		t.Fatal("retry did not commit recovery once")
	}
}
func recoveryRequest(seq uint64, indices ...uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, 1)
	b = wire.AppendVarint(b, 3, 7)
	for _, i := range indices {
		b = wire.AppendVarint(b, 4, i)
	}
	return b
}

func TestInnPartialRecoveryAndReplayUsePersistedHealth(t *testing.T) {
	s, chars, wallet, store := innTestService(t, 2, 21, 1)
	b := recoveryRequest(1, 71, 72)
	code, response, handled, err := s.Handle("/CharAllRevival", b)
	if err != nil || !handled || code != 11 {
		t.Fatal(err)
	}
	hp, _ := chars.CurrentHealth(71)
	second, _ := chars.CurrentHealth(72)
	if hp != 50 || second != 10 || wallet.Snapshot().Gold != 0 {
		t.Fatalf("partial recovery hp=%d second=%d wallet=%+v", hp, second, wallet.Snapshot())
	}
	next, err := New(store, chars, wallet, s.context, s.level, s.battle)
	if err != nil {
		t.Fatal(err)
	}
	next.BeginSession("test")
	_, replay, _, err := next.Handle("/CharAllRevival", b)
	if err != nil || !bytes.Equal(response, replay) {
		t.Fatal("receipt lost on reopening")
	}
	changed := recoveryRequest(1, 72)
	if _, _, _, err = next.Handle("/CharAllRevival", changed); err == nil {
		t.Fatal("changed recovery replay accepted")
	}
}

func TestInnFreeLevelDeadRevivalAndDiscountPrice(t *testing.T) {
	s, chars, wallet, _ := innTestService(t, 0, 20, 1)
	if err := chars.SetCurrentHealth(72, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/CharAllRevival", recoveryRequest(1, 71, 72)); err != nil {
		t.Fatal(err)
	}
	a, _ := chars.CurrentHealth(71)
	b, _ := chars.CurrentHealth(72)
	if a != 100 || b != 1 || wallet.Snapshot().Gold != 0 {
		t.Fatal("free squad recovery or free 1 HP resurrection incorrect")
	}
	s, chars, wallet, _ = innTestService(t, 10, 21, 2)
	if _, _, _, err := s.Handle("/CharAllRevival", recoveryRequest(1, 71)); err != nil {
		t.Fatal(err)
	}
	a, _ = chars.CurrentHealth(71)
	if a != 100 || wallet.Snapshot().Gold != 5 {
		t.Fatal("discount recovery must ceil 90 * 2 /40 *90%=4.05 to 5 gold")
	}
	if _, _, _, err := s.Handle("/CharAllRevival", recoveryRequest(2, 71, 71)); err == nil {
		t.Fatal("duplicate targets accepted")
	}
}
