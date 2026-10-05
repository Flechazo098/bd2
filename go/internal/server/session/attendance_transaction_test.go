package session

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/cryptox"
	"bd2server/internal/server/events"
	"bd2server/internal/server/eventtasks"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/protocol"
	"bd2server/internal/server/wire"
)

// This adapter writes the real normalized wallet and inventory grant ledgers.
type attendanceTransactionEconomy struct {
	wallet    *player.Wallet
	inventory *player.Inventory
	calls     int
}

func (e *attendanceTransactionEconomy) Apply(identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	if len(costs) != 0 {
		return nil, fmt.Errorf("unexpected attendance costs")
	}
	e.calls++
	if _, err := e.wallet.GrantQuestOnce(identity, rewards); err != nil {
		return nil, err
	}
	var items []gamedata.BattleReward
	for _, r := range rewards {
		if r.Type == 9 || r.Type == 8 {
			items = append(items, gamedata.BattleReward{Type: r.Type, ID: r.ID, Count: r.Count})
		}
	}
	if _, err := e.inventory.GrantOnce(identity, items); err != nil {
		return nil, err
	}
	return wire.AppendBytes(nil, 1, nil), nil
}

func TestAttendanceBatchRollsBackAndSuccessfulRetryGrantsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	now := time.Now().UTC()
	registry := events.NewRegistry()
	if err := registry.Replace([]events.Schedule{
		{UID: 1, Type: 0, ID: 1, Start: now.Add(-24 * time.Hour).UnixMilli(), End: now.Add(48 * time.Hour).UnixMilli()},
		{UID: 2, Type: 1, ID: 2, Start: now.Truncate(24 * time.Hour).UnixMilli(), End: now.Add(48 * time.Hour).UnixMilli()},
	}); err != nil {
		t.Fatal(err)
	}
	design := &gamedata.EventTasksDesign{
		Attendance:        map[uint64]gamedata.EventAttendance{1: {ID: 1, Group: 1}},
		AttendanceRewards: map[uint64][]gamedata.EventAttendanceReward{1: {{ID: 1, Group: 1, Day: 1, Basic: gamedata.Reward{Type: 4, Count: 100}}}},
		LimitRewards:      map[[2]uint64]uint64{{2, 1}: 987},
	}
	open := func() (*accountstate.Repository, *player.Wallet, *player.Inventory, *eventtasks.Service, *attendanceTransactionEconomy) {
		t.Helper()
		repo, err := accountstate.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := player.OpenWallet(repo, player.Currency{Gold: 12})
		if err != nil {
			t.Fatal(err)
		}
		inv, err := player.OpenInventory(repo, &player.Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		eco := &attendanceTransactionEconomy{wallet: wallet, inventory: inv}
		svc, err := eventtasks.Open(repo, design, registry, eco)
		if err != nil {
			t.Fatal(err)
		}
		return repo, wallet, inv, svc, eco
	}
	makeServer := func(repo *accountstate.Repository, svc *eventtasks.Service) *Server {
		t.Helper()
		server, err := NewServer(fakeLogin{}, svc, &mutatingDomain{store: repo})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.AttachStateStore(repo); err != nil {
			t.Fatal(err)
		}
		return server
	}
	batch := func(server *Server, cookie string, seq uint64, fail bool) error {
		t.Helper()
		requests := []protocol.BatchRequest{{Path: "/Attendance", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, seq))}}
		if fail {
			requests = append(requests, protocol.BatchRequest{Path: "/InjectedFailure", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, seq+1))})
		}
		plain, err := json.Marshal(requests)
		if err != nil {
			t.Fatal(err)
		}
		body, err := cryptox.EncryptBase64(plain, server.KeyForTest())
		if err != nil {
			t.Fatal(err)
		}
		_, err = server.DispatchRaw("/BatchRequest", []byte(body), "s="+cookie)
		return err
	}
	repo, wallet, inv, svc, eco := open()
	if err := wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := inv.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetNewbieStep(0); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Load("eventtasks")
	if err != nil {
		t.Fatal(err)
	}
	server := makeServer(repo, svc)
	session := login(t, server)
	if err := batch(server, session.Cookie, 2, true); err == nil {
		t.Fatal("failed attendance batch accepted")
	}
	if eco.calls != 1 {
		t.Fatalf("attendance did not reach real economy before failure: calls=%d", eco.calls)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	// Rollback fences the old domain objects; reopen every repository/domain.
	repo, wallet, inv, svc, eco = open()
	after, err := repo.Load("eventtasks")
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("attendance progress, claim markers or receipts survived rollback: %s", after)
	}
	if wallet.Snapshot().Gold != 12 || len(inv.All()) != 0 {
		t.Fatalf("rewards survived rollback: wallet=%+v items=%+v", wallet.Snapshot(), inv.All())
	}
	server = makeServer(repo, svc)
	session = login(t, server)
	if err := batch(server, session.Cookie, 2, false); err != nil {
		t.Fatal(err)
	}
	if err := batch(server, session.Cookie, 3, false); err != nil {
		t.Fatal(err)
	}
	if eco.calls != 1 {
		t.Fatalf("new sequence duplicated attendance reward: calls=%d", eco.calls)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, wallet, inv, _, _ = open()
	defer repo.Close()
	items := inv.All()
	if wallet.Snapshot().Gold != 112 || len(items) != 1 || items[0].Type != 9 || items[0].ID != 987 || items[0].Count != 1 {
		t.Fatalf("successful attendance not persisted exactly once: wallet=%+v items=%+v", wallet.Snapshot(), items)
	}
	var state struct {
		Attendance map[string]struct {
			Count             uint64
			LastDay           string
			Obtained, History map[string]bool
		}
		LoginDays map[string]int64
	}
	saved, err := repo.Load("eventtasks")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(saved, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Attendance) != 2 || len(state.LoginDays) != 1 {
		t.Fatalf("successful attendance progress missing: %s", saved)
	}
	for uid, a := range state.Attendance {
		if a.Count != 1 || a.LastDay == "" || len(a.Obtained) != 1 || len(a.History) != 1 {
			t.Fatalf("claim markers missing for %s: %+v", uid, a)
		}
	}
}
