package session

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/cryptox"
	"bd2server/internal/server/events"
	"bd2server/internal/server/eventtasks"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/mail"
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
			items = append(items, gamedata.BattleReward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
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
	open := func() (*accountstate.Repository, *player.Wallet, *player.Inventory, *eventtasks.Service, *mail.Service, *attendanceTransactionEconomy) {
		t.Helper()
		repo, err := accountstate.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Close() })
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
		mailbox, err := mail.OpenService(repo, &mail.Starter{Version: "2.35.10", MailCount: 1}, inv, wallet)
		if err != nil {
			t.Fatal(err)
		}
		if err := mailbox.AttachAttendanceRewardEconomy(eco); err != nil {
			t.Fatal(err)
		}
		svc.AttachAttendanceMail(mailbox)
		return repo, wallet, inv, svc, mailbox, eco
	}
	makeServer := func(repo *accountstate.Repository, svc *eventtasks.Service, mailbox *mail.Service) *Server {
		t.Helper()
		server, err := NewServer(fakeLogin{}, svc, mailbox, &mutatingDomain{store: repo})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.AttachStateStore(repo); err != nil {
			t.Fatal(err)
		}
		return server
	}
	batch := func(server *Server, cookie, endpoint string, seq uint64, fail bool) error {
		t.Helper()
		request := wire.AppendVarint(nil, 1, seq)
		if endpoint == "/MailOpen" {
			request = wire.AppendVarint(request, 2, 1)
		}
		requests := []protocol.BatchRequest{{Path: endpoint, RequestData: base64.StdEncoding.EncodeToString(request)}}
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
	mailState := func(repo *accountstate.Repository) map[string]any {
		t.Helper()
		result := map[string]any{}
		core, err := repo.Load("mail")
		if err != nil {
			t.Fatal(err)
		}
		result["core"] = string(core)
		for _, bucket := range []string{"dynamic", "issued", "history"} {
			entries, err := repo.ListEntries("mail", bucket)
			if err != nil {
				t.Fatal(err)
			}
			result[bucket] = entries
		}
		return result
	}
	assertUnclaimed := func(wallet *player.Wallet, inv *player.Inventory) {
		t.Helper()
		if wallet.Snapshot().Gold != 12 || len(inv.All()) != 0 {
			t.Fatalf("unclaimed mail credited rewards: wallet=%+v items=%+v", wallet.Snapshot(), inv.All())
		}
	}
	repo, wallet, inv, svc, mailbox, eco := open()
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
	beforeMail := mailState(repo)
	server := makeServer(repo, svc, mailbox)
	session := login(t, server)
	if err := batch(server, session.Cookie, "/Attendance", 2, true); err == nil {
		t.Fatal("failed attendance batch accepted")
	}
	if eco.calls != 0 {
		t.Fatalf("attendance credited economy instead of mailing rewards: calls=%d", eco.calls)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	// Rollback fences the old domain objects; reopen every repository/domain.
	repo, wallet, inv, svc, mailbox, eco = open()
	after, err := repo.Load("eventtasks")
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("attendance progress, claim markers or receipts survived rollback: %s", after)
	}
	if !reflect.DeepEqual(mailState(repo), beforeMail) {
		t.Fatal("attendance mail or issued ledger survived rollback")
	}
	assertUnclaimed(wallet, inv)
	server = makeServer(repo, svc, mailbox)
	session = login(t, server)
	if err := batch(server, session.Cookie, "/Attendance", 2, false); err != nil {
		t.Fatal(err)
	}
	if err := batch(server, session.Cookie, "/Attendance", 3, false); err != nil {
		t.Fatal(err)
	}
	if eco.calls != 0 {
		t.Fatalf("attendance mail prematurely credited economy: calls=%d", eco.calls)
	}
	assertUnclaimed(wallet, inv)
	issuedMail := mailState(repo)
	if len(issuedMail["dynamic"].(map[string][]byte)) != 1 || len(issuedMail["issued"].(map[string][]byte)) != 1 {
		t.Fatal("attendance did not issue exactly one mail")
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, wallet, inv, svc, mailbox, eco = open()
	server = makeServer(repo, svc, mailbox)
	session = login(t, server)
	if err := batch(server, session.Cookie, "/Attendance", 2, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mailState(repo), issuedMail) {
		t.Fatal("relogin duplicated attendance mail")
	}
	assertUnclaimed(wallet, inv)
	if err := batch(server, session.Cookie, "/MailOpen", 3, true); err == nil {
		t.Fatal("failed mailbox batch accepted")
	}
	if eco.calls != 1 {
		t.Fatalf("mail claim did not reach real economy before rollback: calls=%d", eco.calls)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, wallet, inv, svc, mailbox, _ = open()
	if !reflect.DeepEqual(mailState(repo), issuedMail) {
		t.Fatal("mail claim/history persisted after failed batch")
	}
	assertUnclaimed(wallet, inv)
	server = makeServer(repo, svc, mailbox)
	session = login(t, server)
	if err := batch(server, session.Cookie, "/MailOpen", 2, false); err != nil {
		t.Fatal(err)
	}
	if err := batch(server, session.Cookie, "/MailOpen", 2, false); err != nil {
		t.Fatal(err)
	}
	if err := batch(server, session.Cookie, "/MailOpen", 3, false); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 112 || len(inv.All()) != 1 || inv.All()[0].Count != 1 {
		t.Fatal("mail replay duplicated persisted rewards")
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, wallet, inv, svc, mailbox, _ = open()
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	items := inv.All()
	if wallet.Snapshot().Gold != 112 || len(items) != 1 || items[0].Type != 9 || items[0].ID != 987 || items[0].Count != 1 {
		t.Fatalf("successful attendance not persisted exactly once: wallet=%+v items=%+v", wallet.Snapshot(), items)
	}
	for _, domain := range []string{"wallet", "items"} {
		grants, err := repo.ListEntries(domain, "granted")
		if err != nil {
			t.Fatal(err)
		}
		if len(grants) != 1 {
			t.Fatalf("%s durable grant ledger duplicated or missing: %v", domain, grants)
		}
	}
	claimedMail := mailState(repo)
	var claimed struct {
		Opened []uint64 `json:"opened"`
	}
	if err := json.Unmarshal([]byte(claimedMail["core"].(string)), &claimed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(claimed.Opened, []uint64{1}) || len(claimedMail["history"].(map[string][]byte)) != 1 {
		t.Fatal("mail claim/history was not persisted")
	}
	server = makeServer(repo, svc, mailbox)
	session = login(t, server)
	if err := batch(server, session.Cookie, "/MailOpen", 2, false); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 112 || len(inv.All()) != 1 || inv.All()[0].Count != 1 {
		t.Fatal("reopened mail claim duplicated rewards")
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
