package player

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func connectTestService(t *testing.T, store stateio.Store) (*CostumePotentialService, *CharacterStore) {
	t.Helper()
	inv, e := OpenInventory(store, &Starter{Version: "2.35.10"})
	if e != nil {
		t.Fatal(e)
	}
	chars, e := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 91, Level: 1, HP: 80}}, inv, "", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = chars.EnsurePersisted(); e != nil {
		t.Fatal(e)
	}
	collection, e := OpenCollectionStore(store, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(collection.Costumes()) == 0 {
		v := cloneCollection(collection.data)
		v.Costumes = []Costume{{InvenIndex: 88, ID: 100, UseChar: 77}, {InvenIndex: 89, ID: 200, UseChar: 77}, {InvenIndex: 90, ID: 300, UseChar: 77}}
		if e = collection.commit(v); e != nil {
			t.Fatal(e)
		}
	}
	wallet, e := OpenWallet(store, Currency{Gold: 100})
	if e != nil {
		t.Fatal(e)
	}
	d := &gamedata.CostumePotentialDesign{CharacterUnique: map[uint64]uint64{91: 9}, CostumeUnique: map[uint64]uint64{100: 9, 200: 9, 300: 10}, Nodes: map[uint64]map[uint64]gamedata.CostumePotentialNode{100: {1: {ID: 1}}, 200: {1: {ID: 1}}, 300: {1: {ID: 1}}}}
	s, e := NewCostumePotentialService(d, collection, chars, inv, wallet)
	d.CharacterTypes = map[uint64]uint64{91: 0}
	d.CostumeActive = map[uint64]bool{100: true, 200: true, 300: true}
	if e != nil {
		t.Fatal(e)
	}
	s.AttachConnectStore(store)
	s.BeginSession("test")
	chars.AttachMaxHealth(func(c Character) (uint64, error) {
		if c.ConnectPotentialCostume == 200 {
			return 50, nil
		}
		return 100, nil
	})
	return s, chars
}
func connectRequest(seq uint64, rows ...[2]uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	for _, r := range rows {
		v := wire.AppendVarint(wire.AppendVarint(nil, 1, r[0]), 2, r[1])
		b = wire.AppendBytes(b, 2, v)
	}
	return b
}
func TestPotentialConnectFreeZeroNodesSwitchClampsAndReplaysAcrossRestart(t *testing.T) {
	store := stateio.NewMemory()
	s, chars := connectTestService(t, store)
	if _, _, _, e := s.Handle("/CostumePotentialConnect", connectRequest(1, [2]uint64{77, 100})); e != nil {
		t.Fatal(e)
	}
	c, _ := chars.Find(77)
	if c.ConnectPotentialCostume != 100 || c.HP != 80 || s.wallet.Snapshot().Gold != 100 {
		t.Fatal("auto connection charged or healed")
	}
	b := connectRequest(2, [2]uint64{77, 200})
	_, reply, _, e := s.Handle("/CostumePotentialConnect", b)
	if e != nil {
		t.Fatal(e)
	}
	c, _ = chars.Find(77)
	if c.HP != 50 {
		t.Fatal("maxHP decrease not clamped")
	}
	next, _ := connectTestService(t, store)
	_, again, _, e := next.Handle("/CostumePotentialConnect", b)
	if e != nil || !bytes.Equal(reply, again) {
		t.Fatal("restart lost exact connection replay")
	}
	if _, _, _, e = next.Handle("/CostumePotentialConnect", connectRequest(3, [2]uint64{77, 100})); e != nil {
		t.Fatal(e)
	}
	c, _ = next.characters.Find(77)
	if c.HP != 50 {
		t.Fatal("maxHP increase healed player")
	}
	if e = next.characters.SetCurrentHealth(77, 0); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = next.Handle("/CostumePotentialConnect", connectRequest(4, [2]uint64{77, 200})); e != nil {
		t.Fatal(e)
	}
	c, _ = next.characters.Find(77)
	if c.HP != 0 {
		t.Fatal("connection revived dead character")
	}
}
func TestPotentialConnectRejectsUnownedOtherCharacterAndDuplicateBatch(t *testing.T) {
	s, chars := connectTestService(t, stateio.NewMemory())
	for _, b := range [][]byte{connectRequest(1, [2]uint64{77, 300}), connectRequest(2, [2]uint64{77, 88}), connectRequest(3, [2]uint64{77, 0}), connectRequest(4, [2]uint64{77, 100}, [2]uint64{77, 200}), connectRequest(5, [2]uint64{77, 100}, [2]uint64{999, 200})} {
		if _, _, _, e := s.Handle("/CostumePotentialConnect", b); e == nil {
			t.Fatal("invalid potential connection accepted")
		}
	}
	c, _ := chars.Find(77)
	if c.ConnectPotentialCostume != 0 {
		t.Fatal("invalid batch partially updated character")
	}
	s.design.CostumeActive[100] = false
	if _, _, _, e := s.Handle("/CostumePotentialConnect", connectRequest(6, [2]uint64{77, 100})); e == nil {
		t.Fatal("inactive potential costume connected")
	}
	s.design.CostumeActive[100] = true
	s.design.CharacterTypes[91] = 1
	if _, _, _, e := s.Handle("/CostumePotentialConnect", connectRequest(7, [2]uint64{77, 100})); e == nil {
		t.Fatal("temporary design character connected")
	}
	s.design.CharacterTypes[91] = 0
	duplicate := wire.AppendVarint(connectRequest(8, [2]uint64{77, 100}), 1, 9)
	if _, _, _, e := s.Handle("/CostumePotentialConnect", duplicate); e == nil {
		t.Fatal("duplicate sequence accepted")
	}
	row := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 77), 2, 100), 2, 200)
	duplicate = wire.AppendBytes(wire.AppendVarint(nil, 1, 10), 2, row)
	if _, _, _, e := s.Handle("/CostumePotentialConnect", duplicate); e == nil {
		t.Fatal("duplicate nested costume scalar accepted")
	}
}

type connectFailStore struct{ stateio.Store }

func (s connectFailStore) Save(name string, b []byte) error {
	if name == "potentialconnect" {
		return errors.New("receipt failed")
	}
	return s.Store.Save(name, b)
}
func TestPotentialConnectSQLiteFailureRollsBackLinkAndHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, e := accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s, _ := connectTestService(t, repo)
	s.AttachConnectStore(connectFailStore{repo})
	op, e := repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	b := connectRequest(1, [2]uint64{77, 200})
	if _, _, _, e = s.Handle("/CostumePotentialConnect", b); e == nil {
		t.Fatal("receipt failure ignored")
	}
	if e = op.Rollback(); e != nil && !errors.Is(e, stateio.ErrStateRecoveryRequired) {
		t.Fatal(e)
	}
	repo.Close()
	repo, e = accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer repo.Close()
	next, chars := connectTestService(t, repo)
	c, _ := chars.Find(77)
	if c.ConnectPotentialCostume != 0 || c.HP != 80 {
		t.Fatal("failed connection persisted link/HP")
	}
	op, e = repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = next.Handle("/CostumePotentialConnect", b); e != nil {
		t.Fatal(e)
	}
	if e = op.Commit(); e != nil {
		t.Fatal(e)
	}
}

func TestPotentialConnectionCollectionCharacterFallbackPersistsAcrossRestart(t *testing.T) {
	store := stateio.NewMemory()
	s, chars := connectTestService(t, store)
	v := cloneCollection(s.collection.data)
	v.Characters = []Character{{InvenIndex: 78, ID: 92, Level: 1, HP: 40, CostumeID: 201, UseCostume: 91}}
	v.Costumes = append(v.Costumes, Costume{InvenIndex: 91, ID: 201, UseChar: 78})
	if e := s.collection.commit(v); e != nil {
		t.Fatal(e)
	}
	if e := chars.AttachCollection(s.collection); e != nil {
		t.Fatal(e)
	}
	s.design.CharacterUnique[92] = 9
	s.design.CharacterTypes[92] = 0
	s.design.CostumeUnique[201] = 9
	s.design.CostumeActive[201] = true
	s.design.Nodes[201] = map[uint64]gamedata.CostumePotentialNode{1: {ID: 1}}
	b := connectRequest(1, [2]uint64{78, 201})
	_, response, _, e := s.Handle("/CostumePotentialConnect", b)
	if e != nil {
		t.Fatal(e)
	}
	c, ok := chars.Find(78)
	if !ok || c.ConnectPotentialCostume != 201 || c.UseCostume != 91 || c.CostumeID != 201 || c.HP != 40 {
		t.Fatal("collection connection changed appearance or HP")
	}
	collection, e := OpenCollectionStore(store, nil)
	if e != nil {
		t.Fatal(e)
	}
	c, ok = collection.FindCharacter(78)
	if !ok || c.ConnectPotentialCostume != 201 {
		t.Fatal("collection connection lost after restart")
	}
	s.collection = collection
	chars.AttachCollection(collection)
	s.AttachConnectStore(store)
	s.BeginSession("test")
	_, again, _, e := s.Handle("/CostumePotentialConnect", b)
	if e != nil || !bytes.Equal(response, again) {
		t.Fatal("collection reconnect replay lost")
	}
}
