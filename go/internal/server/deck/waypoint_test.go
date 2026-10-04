package deck

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"testing"
)

func waypointFixture(t *testing.T) (*Store, *stateio.Memory) {
	t.Helper()
	memory := stateio.NewMemory()
	seededStore := seeded(t)
	s, err := OpenStore(memory, Seed{Version: seededStore.state.Version, FieldDeck: seededStore.state.FieldDeck, FieldCharControlDeckType: seededStore.state.FieldCharControlDeckType})
	if err != nil {
		t.Fatal(err)
	}
	configureWaypointFixture(t, s)
	return s, memory
}
func configureWaypointFixture(t *testing.T, s *Store) {
	t.Helper()
	if err := s.ConfigureWaypoints(func(uint64) (gamedata.WaypointPack, error) {
		return gamedata.WaypointPack{Points: map[uint64]gamedata.Waypoint{1: {ID: 1, MapID: 10}, 2: {ID: 2, MapID: 20}}, PriceType: 4, PriceUnit: 7}, nil
	}, func(pack uint64, _ bool) error {
		if pack == 3 {
			return errors.New("locked pack")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func waypointReq(seq, pack, start, end uint64) []byte {
	body := wire.AppendVarint(nil, 2, pack)
	if start != 0 {
		body = wire.AppendVarint(body, 3, start)
	}
	if end != 0 {
		body = wire.AppendVarint(body, 4, end)
		body = wire.AppendVarint(body, 5, 1)
	}
	return req(seq, body)
}

func TestWaypointActivationPersistsAllIDsAndInfoScopesPack(t *testing.T) {
	s, memory := waypointFixture(t)
	for _, r := range []struct{ seq, pack, id uint64 }{{1, 1, 2}, {2, 1, 1}, {3, 2, 2}, {4, 1, 1}} {
		if _, _, _, err := s.Handle("/WaypointSave", waypointReq(r.seq, r.pack, r.id, 0)); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenStore(memory, Seed{Version: s.state.Version, FieldDeck: s.state.FieldDeck, FieldCharControlDeckType: s.state.FieldCharControlDeckType})
	if err != nil {
		t.Fatal(err)
	}
	configureWaypointFixture(t, reopened)
	code, body, _, err := reopened.Handle("/WaypointInfo", waypointReq(5, 1, 0, 0))
	if err != nil || code != 31 || !bytes.Equal(body, []byte{10, 2, 1, 2}) {
		t.Fatalf("pack1 info %d %x %v", code, body, err)
	}
	_, body, _, err = reopened.Handle("/WaypointInfo", waypointReq(6, 2, 0, 0))
	if err != nil || !bytes.Equal(body, []byte{10, 1, 2}) {
		t.Fatalf("pack2 info %x %v", body, err)
	}
	if len(reopened.state.Waypoints[1]) != 2 {
		t.Fatal("duplicate activation persisted")
	}
}

func TestWaypointTravelChargesOnceAndRejectsInvalidBeforeSpending(t *testing.T) {
	s, memory := waypointFixture(t)
	wallet, err := player.OpenWallet(memory, player.Currency{Gold: 14})
	if err != nil {
		t.Fatal(err)
	}
	s.wallet = wallet
	s.BeginSession("test-session")
	for i := uint64(1); i <= 2; i++ {
		if _, _, _, err = s.Handle("/WaypointSave", waypointReq(i, 1, i, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range [][]byte{waypointReq(3, 3, 1, 2), waypointReq(4, 1, 1, 99), waypointReq(5, 2, 1, 2)} {
		if _, _, _, err = s.Handle("/WaypointUse", request); err == nil {
			t.Fatal("invalid travel accepted")
		}
		if wallet.Snapshot().Gold != 14 {
			t.Fatal("invalid travel charged")
		}
	}
	valid := waypointReq(6, 1, 1, 2)
	for i := 0; i < 2; i++ {
		code, _, _, err := s.Handle("/WaypointUse", valid)
		if err != nil || code != 33 {
			t.Fatalf("travel %d %v", code, err)
		}
	}
	if wallet.Snapshot().Gold != 7 {
		t.Fatal("repeat travel double charged")
	}
	reopened, err := OpenStore(memory, Seed{Version: s.state.Version, FieldDeck: s.state.FieldDeck, FieldCharControlDeckType: s.state.FieldCharControlDeckType})
	if err != nil {
		t.Fatal(err)
	}
	configureWaypointFixture(t, reopened)
	reopened.wallet = wallet
	reopened.BeginSession("test-session")
	if _, _, _, err = reopened.Handle("/WaypointUse", valid); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 7 {
		t.Fatal("restart retry charged")
	}
	if _, _, _, err = reopened.Handle("/WaypointUse", waypointReq(7, 1, 2, 1)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = reopened.Handle("/WaypointUse", waypointReq(8, 1, 1, 2)); err == nil {
		t.Fatal("insufficient balance accepted")
	}
	if wallet.Snapshot().Gold != 0 {
		t.Fatal("failed debit changed wallet")
	}
}
