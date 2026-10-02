package mail

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func spoolTestService(t *testing.T, store stateio.Store) (*Service, *player.Inventory, *player.Wallet) {
	t.Helper()
	inventory, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(store, &Starter{Version: "2.35.10", MailCount: 1, MaxMailID: 100}, inventory, wallet)
	if err != nil {
		t.Fatal(err)
	}
	return service, inventory, wallet
}

func spoolGrant(identity string, rewards ...GrantReward) Grant {
	return Grant{Identity: identity, Title: "系统奖励", Body: "请领取邮件附件。", SentAt: time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC).UnixMilli(), Rewards: rewards}
}

func writeSpool(t *testing.T, path string, grants ...Grant) {
	t.Helper()
	data, err := json.Marshal(GrantSpool{Version: 1, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGrantSpoolImportsDynamicMailOnceAcrossOpenAndRestart(t *testing.T) {
	store := stateio.NewMemory()
	service, _, wallet := spoolTestService(t, store)
	path := filepath.Join(t.TempDir(), "grants.json")
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	if _, _, _, err := service.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	}
	writeSpool(t, path,
		spoolGrant("dev-mail:a", GrantReward{Type: 4, Count: 123}),
		spoolGrant("dev-mail:b", GrantReward{Type: 8, ID: 9, Count: 10}),
	)
	// Spool attachment never changes the versioned starter mailbox.
	if len(service.Starter.Mails) != 0 {
		t.Fatal("starter changed")
	}
	for i := 0; i < 2; i++ {
		code, response, handled, err := service.Handle("/MailInfo", request)
		if err != nil || !handled || code != packetCode {
			t.Fatalf("code=%d handled=%v err=%v", code, handled, err)
		}
		count, _, _ := wire.Varint(response, 2)
		max, _, _ := wire.Varint(response, 3)
		if count != 3 || max != 102 {
			t.Fatalf("count=%d max=%d", count, max)
		}
	}
	if wallet.Snapshot().Gold != 0 {
		t.Fatal("unopened mail granted currency")
	}
	open := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 101)
	if _, _, _, err := service.Handle("/MailOpen", open); err != nil {
		t.Fatal(err)
	}
	reopened, inventory, wallet := spoolTestService(t, store)
	if err := reopened.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	_, response, _, err := reopened.Handle("/MailInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	if count, _, _ := wire.Varint(response, 2); count != 2 {
		t.Fatalf("count=%d", count)
	}
	if len(reopened.dynamic) != 2 || reopened.state.NextDynamicMailID != 103 {
		t.Fatalf("reissued mail: %+v", reopened.state)
	}
	open = wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, 102)
	if _, _, _, err := reopened.Handle("/MailOpen", open); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 123 || len(inventory.All()) != 1 || inventory.All()[0].Count != 10 {
		t.Fatalf("wallet=%+v inventory=%+v", wallet.Snapshot(), inventory.All())
	}
	if _, _, _, err := reopened.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	}
	if reopened.state.NextDynamicMailID != 103 {
		t.Fatal("opened grants were reissued")
	}
}

func TestGrantSpoolPersistsIssuedIdentityInSQLiteRequestTransaction(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "state.db")
	path := filepath.Join(dir, "grants.json")
	writeSpool(t, path, spoolGrant("dev-mail:sqlite", GrantReward{Type: 4, Count: 123}))
	repository, err := accountstate.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	service, _, _ := spoolTestService(t, repository)
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	operation, err := repository.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	if _, _, _, err := service.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	}
	if err := operation.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = accountstate.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	service, _, _ = spoolTestService(t, repository)
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	operation, err = repository.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	}
	if err := operation.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(service.dynamic) != 1 || service.issued["dev-mail:sqlite"] != 101 || service.state.NextDynamicMailID != 102 {
		t.Fatalf("SQLite restart reissued grant: state=%+v issued=%+v", service.state, service.issued)
	}
}

func TestGrantSpoolPaidJewelryAndDrawTicketsClaimAndRestart(t *testing.T) {
	store := stateio.NewMemory()
	service, _, _ := spoolTestService(t, store)
	path := filepath.Join(t.TempDir(), "grants.json")
	writeSpool(t, path, spoolGrant("paid-and-draw-tickets",
		GrantReward{Type: 2, Count: 100000000},
		GrantReward{Type: 8, ID: 1000, Count: 100000000},
		GrantReward{Type: 8, ID: 1104, Count: 100000000},
		GrantReward{Type: 4, Count: 1000000000},
	))
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 1)); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 101)
	_, response, _, err := service.Handle("/MailOpen", request)
	if err != nil {
		t.Fatal(err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("reward bundle missing: %v", err)
	}
	items := map[[2]uint64]uint64{}
	views := map[[2]uint64]uint64{}
	if err := wire.Walk(bundle, func(field wire.Field) error {
		if field.Number != 1 && field.Number != 6 {
			t.Fatalf("unexpected reward bundle field %d", field.Number)
		}
		id, _, err := wire.Varint(field.Value, 2)
		if err != nil {
			return err
		}
		typ, _, err := wire.Varint(field.Value, 3)
		if err != nil {
			return err
		}
		count, _, err := wire.Varint(field.Value, 4)
		if err != nil {
			return err
		}
		index, _, err := wire.Varint(field.Value, 1)
		if err != nil {
			return err
		}
		if field.Number == 1 {
			if typ == 8 && index == 0 || typ != 8 && index != 0 {
				t.Fatalf("incorrect item instance for type=%d index=%d", typ, index)
			}
			items[[2]uint64{typ, id}] = count
		} else {
			views[[2]uint64{typ, id}] = count
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 || items[[2]uint64{2, 0}] != 100000000 || items[[2]uint64{4, 0}] != 1000000000 ||
		items[[2]uint64{8, 1000}] != 100000000 || items[[2]uint64{8, 1104}] != 100000000 ||
		len(views) != 2 || views[[2]uint64{8, 1000}] != 100000000 || views[[2]uint64{8, 1104}] != 100000000 {
		t.Fatalf("items=%v views=%v", items, views)
	}
	service, inventory, wallet := spoolTestService(t, store)
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 3)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	if got := wallet.Snapshot(); got.Jewelry != 100000000 || got.Gold != 1000000000 || len(inventory.All()) != 2 || service.state.NextDynamicMailID != 102 {
		t.Fatalf("grant duplicated after restart: wallet=%+v items=%+v state=%+v", got, inventory.All(), service.state)
	}
}

func TestGrantSpoolEnforcesMailInt32IDAndCountBoundaries(t *testing.T) {
	for _, reward := range []GrantReward{
		{Type: 8, ID: math.MaxInt32, Count: math.MaxInt32},
		{Type: 2, Count: math.MaxInt32},
		{Type: 19, ID: 450030, Count: 1},
	} {
		input, err := json.Marshal(GrantSpool{Version: 1, Grants: []Grant{spoolGrant("boundary", reward)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeGrantSpool(input); err != nil {
			t.Fatalf("valid boundary reward rejected: %+v: %v", reward, err)
		}
	}
	for _, reward := range []GrantReward{
		{Type: 8, ID: math.MaxInt32 + 1, Count: 1},
		{Type: 8, ID: 1000, Count: math.MaxInt32 + 1},
		{Type: 2, ID: 1, Count: 1},
		{Type: 2, Count: math.MaxInt32 + 1},
		{Type: 19, ID: 450031, Count: 1},
		{Type: 19, ID: 450030, Count: 0},
		{Type: 19, ID: 450030, Count: 2},
	} {
		input, err := json.Marshal(GrantSpool{Version: 1, Grants: []Grant{spoolGrant("boundary", reward)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeGrantSpool(input); err == nil {
			t.Fatalf("invalid boundary reward accepted: %+v", reward)
		}
	}
}

func TestGrantSpoolOneUseContentTicketItemDBInfoPersistsAndIsIdempotent(t *testing.T) {
	// In 2.35.10 CommonPacket.AddItemInfo dispatches ElementType 19 to
	// AddContentTicketItem; it consumes a normal ItemDBInfo with an instance ID.
	store := stateio.NewMemory()
	service, inventory, _ := spoolTestService(t, store)
	path := filepath.Join(t.TempDir(), "grants.json")
	writeSpool(t, path, spoolGrant("full-moon-one-use", GrantReward{Type: 19, ID: 450030, Count: 1}))
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 1)); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 101)
	_, response, _, err := service.Handle("/MailOpen", request)
	if err != nil {
		t.Fatal(err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("reward bundle missing: %v", err)
	}
	item, found, err := wire.Bytes(bundle, 1)
	if err != nil || !found {
		t.Fatalf("ItemDBInfo missing: %v", err)
	}
	for field, want := range map[int]uint64{2: 450030, 3: 19, 4: 1} {
		if got, present, err := wire.Varint(item, field); err != nil || !present || got != want {
			t.Fatalf("ItemDBInfo field %d=%d present=%v err=%v", field, got, present, err)
		}
	}
	index, _, err := wire.Varint(item, 1)
	if err != nil || index == 0 {
		t.Fatalf("content ticket instance=%d err=%v", index, err)
	}
	if got := inventory.All(); len(got) != 1 || got[0].Type != 19 || got[0].ID != 450030 || got[0].Count != 1 || got[0].InvenIndex != index {
		t.Fatalf("content ticket not stored: %+v", got)
	}
	service, inventory, _ = spoolTestService(t, store)
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 3)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	if got := inventory.All(); len(got) != 1 || got[0].Count != 1 || got[0].InvenIndex != index || service.state.NextDynamicMailID != 102 {
		t.Fatalf("content ticket reissued: items=%+v state=%+v", got, service.state)
	}
}

func TestGrantSpoolValidatesEntireBatchBeforeIssuing(t *testing.T) {
	store := stateio.NewMemory()
	service, _, _ := spoolTestService(t, store)
	path := filepath.Join(t.TempDir(), "grants.json")
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	good := spoolGrant("a", GrantReward{Type: 4, Count: 10})
	bad := spoolGrant("b", GrantReward{Type: 6, ID: 1, Count: 1})
	writeSpool(t, path, good, bad)
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 1)); err == nil {
		t.Fatal("accepted unsupported second grant")
	}
	assertNoSpoolMail(t, service, store)
	writeSpool(t, path, good, good)
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 2)); err == nil {
		t.Fatal("accepted duplicate identity")
	}
	assertNoSpoolMail(t, service, store)
	writeSpool(t, path, good)
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 3)); err != nil {
		t.Fatal(err)
	}
	if service.issued["a"] != 101 {
		t.Fatal("valid replacement did not import")
	}
}

func assertNoSpoolMail(t *testing.T, service *Service, store stateio.EntryStore) {
	t.Helper()
	if len(service.dynamic) != 0 || len(service.issued) != 0 || service.state.NextDynamicMailID != 101 {
		t.Fatalf("partial memory update: %+v", service.state)
	}
	for _, bucket := range []string{"dynamic", "issued"} {
		entries, err := store.ListEntries("mail", bucket)
		if err != nil || len(entries) != 0 {
			t.Fatalf("partial storage %s: entries=%v err=%v", bucket, entries, err)
		}
	}
}

type failingSpoolStore struct {
	*stateio.Memory
	fail   bool
	writes int
}

func (s *failingSpoolStore) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if domain == "mail" && len(changes) != 0 {
		s.writes++
		if s.fail {
			return errors.New("injected spool save failure")
		}
	}
	return s.Memory.SaveWithEntries(domain, core, changes)
}

func TestGrantSpoolBatchWriteFailureLeavesMemoryAndStorageUnchanged(t *testing.T) {
	store := &failingSpoolStore{Memory: stateio.NewMemory(), fail: true}
	service, _, _ := spoolTestService(t, store)
	path := filepath.Join(t.TempDir(), "grants.json")
	writeSpool(t, path, spoolGrant("a", GrantReward{Type: 4, Count: 10}), spoolGrant("b", GrantReward{Type: 4, Count: 20}))
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	if _, _, _, err := service.Handle("/MailInfo", request); err == nil {
		t.Fatal("save failure ignored")
	}
	assertNoSpoolMail(t, service, store)
	if store.writes != 1 {
		t.Fatalf("batch used %d writes", store.writes)
	}
	store.fail = false
	if _, _, _, err := service.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	}
	if len(service.dynamic) != 2 || service.state.NextDynamicMailID != 103 {
		t.Fatal("retry did not issue complete batch")
	}
}

func TestGrantSpoolBatchIDExhaustionDoesNotIssueFirstMail(t *testing.T) {
	store := stateio.NewMemory()
	service, _, _ := spoolTestService(t, store)
	service.state.NextDynamicMailID = ^uint64(0) - 1
	path := filepath.Join(t.TempDir(), "grants.json")
	writeSpool(t, path, spoolGrant("a", GrantReward{Type: 4, Count: 10}), spoolGrant("b", GrantReward{Type: 4, Count: 20}))
	if err := service.AttachGrantSpoolPath(path); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 1)); err == nil {
		t.Fatal("accepted overflowing batch")
	}
	if len(service.dynamic) != 0 || service.state.NextDynamicMailID != ^uint64(0)-1 {
		t.Fatal("partially imported overflowing batch")
	}
}

func TestGrantSpoolRejectsMalformedSchema(t *testing.T) {
	valid := `{"version":1,"grants":[{"identity":"a","title":"t","body":"b","sent_at":1790989200000,"rewards":[{"type":4,"id":0,"count":10}]}]}`
	if _, err := decodeGrantSpool([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	invalid := []string{
		`null`, `{}`, `{"version":1,"grants":null}`, `{"version":1,"grants":[],"extra":0}`,
		strings.Replace(valid, `"version":1`, `"version":2`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"title":"t"`, `"title":"t","title":"u"`, 1),
		strings.Replace(valid, `"title":"t"`, `"Title":"t"`, 1),
		strings.Replace(valid, `"body":"b"`, `"body":null`, 1),
		strings.Replace(valid, `"identity":"a"`, `"identity":" "`, 1),
		strings.Replace(valid, `"sent_at":1790989200000`, `"sent_at":0`, 1),
		strings.Replace(valid, `"sent_at":1790989200000`, `"sent_at":9223372036854775807`, 1),
		strings.Replace(valid, `"count":10`, `"count":0`, 1),
		strings.Replace(valid, `"count":10`, `"count":1.5`, 1),
		strings.Replace(valid, `"count":10`, `"count":null`, 1),
		strings.Replace(valid, `"count":10`, `"count":10,"count":10`, 1),
		strings.Replace(valid, `"count":10`, `"count":10,"extra":0`, 1),
		strings.Replace(valid, `"id":0,`, ``, 1),
		strings.Replace(valid, `"id":0`, `"id":1`, 1),
		strings.Replace(valid, `"type":4`, `"type":8`, 1),
		valid + `{}`,
	}
	for _, input := range invalid {
		if _, err := decodeGrantSpool([]byte(input)); err == nil {
			t.Errorf("accepted malformed spool: %s", input)
		}
	}
	if _, err := decodeGrantSpool([]byte(`{"version":1,"grants":[]}`)); err != nil {
		t.Fatal(err)
	}
}
