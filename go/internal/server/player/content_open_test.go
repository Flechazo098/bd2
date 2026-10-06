package player

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
)

func contentOpenHarness(t *testing.T, store stateio.Store, prerequisite bool, level uint64) (*ContentOpenService, *Inventory) {
	t.Helper()
	starter := &Starter{Version: versionconfig.State()}
	if prerequisite {
		starter.Items = []Item{{InvenIndex: 100, Type: 19, ID: 41, Count: 1}}
	}
	inventory, err := OpenInventory(store, starter)
	if err != nil {
		t.Fatal(err)
	}
	design := &gamedata.ContentOpeningDesign{Prerequisite: gamedata.ContentOpenRule{TicketID: 41, SquadLevel: 3}, Completion: gamedata.ContentOpenRule{TicketID: 42, SquadLevel: 2}}
	service, err := NewContentOpenService(design, inventory, store, func() (uint64, error) { return level, nil })
	if err != nil {
		t.Fatal(err)
	}
	service.BeginSession("authenticated-session")
	return service, inventory
}

func contentOpenRequest(seq, kind uint64) []byte {
	return wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, kind)
}

func contentOpenTicketCount(inventory *Inventory, id uint64) uint64 {
	var count uint64
	for _, item := range inventory.All() {
		if item.Type == 19 && item.ID == id {
			count += item.Count
		}
	}
	return count
}

func assertContentOpenResponse(t *testing.T, body []byte) {
	t.Helper()
	bundle, present, err := wire.Bytes(body, 1)
	if err != nil || !present {
		t.Fatalf("missing RewardDBInfoBundle: %x %v", body, err)
	}
	item, present, err := wire.Bytes(bundle, 1)
	if err != nil || !present {
		t.Fatalf("missing completion ItemDBInfo: %x %v", bundle, err)
	}
	for field, want := range map[int]uint64{2: 42, 3: 19, 4: 1} {
		got, present, err := wire.Varint(item, field)
		if err != nil || !present || got != want {
			t.Fatalf("ItemDBInfo field %d=%d, want %d", field, got, want)
		}
	}
}

func TestContentOpenTicketOnceAcrossSequencesAndSessions(t *testing.T) {
	store := stateio.NewMemory()
	service, inventory := contentOpenHarness(t, store, true, 3)
	request := contentOpenRequest(1, 1)
	code, body, handled, err := service.Handle("/ContentOpen", request)
	if err != nil || !handled || code != 622 {
		t.Fatalf("open: %d %v %v", code, handled, err)
	}
	assertContentOpenResponse(t, body)
	for _, tc := range []struct {
		session string
		seq     uint64
	}{{"authenticated-session", 1}, {"authenticated-session", 2}, {"another-session", 1}} {
		service.BeginSession(tc.session)
		_, next, _, err := service.Handle("/ContentOpen", contentOpenRequest(tc.seq, 1))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(next, body) {
			t.Fatal("ticket repair/retry returned a different item")
		}
	}
	if contentOpenTicketCount(inventory, 41) != 1 || contentOpenTicketCount(inventory, 42) != 1 {
		t.Fatal("prerequisite consumed or completion duplicated")
	}
	data, err := store.Load("content_open")
	if err != nil || bytes.Contains(data, []byte("authenticated-session")) || bytes.Contains(data, []byte("another-session")) {
		t.Fatal("receipt contains session credentials")
	}
	service.BeginSession("authenticated-session")
	changed := wire.AppendVarint(append([]byte(nil), request...), 3, 7)
	if _, _, _, err := service.Handle("/ContentOpen", changed); err == nil {
		t.Fatal("same sequence accepted different request")
	}
}

func TestContentOpenRequiresSessionAndRepairsOwnedTicket(t *testing.T) {
	store := stateio.NewMemory()
	service, inventory := contentOpenHarness(t, store, false, 0)
	service.BeginSession("")
	if _, _, _, err := service.Handle("/ContentOpen", contentOpenRequest(1, 1)); err == nil {
		t.Fatal("missing authenticated session accepted")
	}
	if _, err := inventory.GrantOnce("existing-completion", []gamedata.BattleReward{{ID: 42, Type: 19, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	service.BeginSession("repair-session")
	_, body, _, err := service.Handle("/ContentOpen", contentOpenRequest(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	assertContentOpenResponse(t, body)
	if contentOpenTicketCount(inventory, 42) != 1 {
		t.Fatal("repair duplicated owned completion")
	}
}

func TestContentOpenRejectsCorruptReceiptAtOpenAndAfterConstruction(t *testing.T) {
	store := stateio.NewMemory()
	service, inventory := contentOpenHarness(t, store, true, 3)
	if err := store.Save("content_open", []byte(`{"receipts":{"credentials:1":{"digest":"bad","body":"CgA="}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/ContentOpen", contentOpenRequest(1, 1)); err == nil {
		t.Fatal("corrupt reloaded receipt accepted")
	}
	if contentOpenTicketCount(inventory, 42) != 0 {
		t.Fatal("corrupt receipts granted completion")
	}
	if _, err := NewContentOpenService(service.design, inventory, store, service.squadLevel); err == nil {
		t.Fatal("corrupt receipt accepted during open")
	}
}

func TestContentOpenRejectsUnqualifiedAndMalformedRequests(t *testing.T) {
	for _, tc := range []struct {
		name         string
		prerequisite bool
		level        uint64
		request      []byte
	}{
		{"missing-prerequisite", false, 3, contentOpenRequest(1, 1)},
		{"level", true, 2, contentOpenRequest(1, 1)},
		{"unknown", true, 3, contentOpenRequest(1, 2)},
		{"zero-type", true, 3, contentOpenRequest(1, 0)},
		{"group-is-not-type", true, 3, contentOpenRequest(1, 23)},
		{"zero-sequence", true, 3, contentOpenRequest(0, 1)},
		{"wrong-wire", true, 3, wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 2, []byte{1})},
		{"truncated-trailing-wire", true, 3, append(contentOpenRequest(1, 1), 0x1a, 0x02, 0x01)},
		{"duplicate-type", true, 3, wire.AppendVarint(contentOpenRequest(1, 1), 2, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := stateio.NewMemory()
			service, inventory := contentOpenHarness(t, store, tc.prerequisite, tc.level)
			_, _, handled, err := service.Handle("/ContentOpen", tc.request)
			if err == nil || !handled {
				t.Fatal("invalid request accepted")
			}
			if contentOpenTicketCount(inventory, 42) != 0 {
				t.Fatal("rejected request granted ticket")
			}
			state, err := store.Load("content_open")
			if err != nil || state != nil {
				t.Fatal("rejected request persisted receipt")
			}
		})
	}
}

type contentOpenFailStore struct {
	stateio.AtomicEntryStore
	fail bool
}

func (s *contentOpenFailStore) Save(name string, payload []byte) error {
	if name == "content_open" && s.fail {
		return errors.New("injected receipt write failure")
	}
	return s.AtomicEntryStore.Save(name, payload)
}

func TestContentOpenSQLiteReceiptRestartAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable-exact-retry", true: "failed-receipt-rolls-back-ticket"}[fail], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			repo, err := accountstate.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			store := &contentOpenFailStore{AtomicEntryStore: repo, fail: fail}
			service, _ := contentOpenHarness(t, store, true, 3)
			op, err := repo.BeginOperation()
			if err != nil {
				t.Fatal(err)
			}
			request := contentOpenRequest(1, 1)
			_, body, _, err := service.Handle("/ContentOpen", request)
			if fail {
				if err == nil {
					t.Fatal("receipt failure ignored")
				}
				if err := op.Rollback(); err == nil {
					t.Fatal("dirty rollback did not request account recovery")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := op.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.Close(); err != nil {
				t.Fatal(err)
			}
			repo, err = accountstate.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := repo.Close(); err != nil {
					t.Error(err)
				}
			}()
			service, inventory := contentOpenHarness(t, repo, true, 3)
			if fail {
				if contentOpenTicketCount(inventory, 42) != 0 {
					t.Fatal("uncommitted completion survived restart")
				}
				data, err := repo.Load("content_open")
				if err != nil || data != nil {
					t.Fatal("failed receipt survived restart")
				}
			}
			op, err = repo.BeginOperation()
			if err != nil {
				t.Fatal(err)
			}
			_, replay, _, err := service.Handle("/ContentOpen", request)
			if err != nil {
				t.Fatal(err)
			}
			if err := op.Commit(); err != nil {
				t.Fatal(err)
			}
			if !fail && !bytes.Equal(body, replay) {
				t.Fatal("exact retry changed after restart")
			}
			assertContentOpenResponse(t, replay)
			if contentOpenTicketCount(inventory, 42) != 1 || contentOpenTicketCount(inventory, 41) != 1 {
				t.Fatal("ticket ownership not preserved exactly once")
			}
		})
	}
}
