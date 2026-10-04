package player

import (
	"bytes"
	"errors"
	"testing"

	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func newMasterProfile(t *testing.T, store stateio.Store) (*MasterTitleService, []byte) {
	t.Helper()
	p, err := progress.OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	core, err := store.Load("progress")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenMasterTitleService(store, "Guest_30738202")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	return s, core
}

func TestMasterTitleSurvivesRestartAndKeepsUnknownBirthday(t *testing.T) {
	store := stateio.NewMemory()
	s, core := newMasterProfile(t, store)
	request := wire.AppendVarint(nil, 1, 1)
	code, body, handled, err := s.Handle("/MasterTitleInfo", request)
	if err != nil || !handled || code != 590 {
		t.Fatalf("profile code=%d handled=%v err=%v", code, handled, err)
	}
	name, found, err := wire.Bytes(body, 1)
	if err != nil || !found || string(name) != "Guest_30738202" {
		t.Fatal("profile name is empty, so IntroUI would force tutorial")
	}
	for _, field := range []int{2, 3} {
		if _, found, _ := wire.Varint(body, field); found {
			t.Fatal("unknown birthday was invented")
		}
	}
	update := wire.AppendString(wire.AppendVarint(nil, 1, 2), 2, "玩家名字")
	update = wire.AppendVarint(wire.AppendVarint(update, 3, 2), 4, 29)
	if code, body, handled, err := s.Handle("/MasterTitleInfoUpdate", update); err != nil || !handled || code != 592 || len(body) != 0 {
		t.Fatalf("update %d %v %v", code, handled, err)
	}
	if _, _, _, err := s.Handle("/MasterTitleInfoUpdate", update); err != nil {
		t.Fatal("idempotent set failed", err)
	}
	s, err = OpenMasterTitleService(store, "anotherDefault")
	if err != nil {
		t.Fatal(err)
	}
	_, body, _, err = s.Handle("/MasterTitleInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	name, _, _ = wire.Bytes(body, 1)
	month, _, _ := wire.Varint(body, 2)
	day, _, _ := wire.Varint(body, 3)
	if string(name) != "玩家名字" || month != 2 || day != 29 {
		t.Fatalf("restart profile name=%s birthday=%d/%d", name, month, day)
	}
	after, _ := store.Load("progress")
	if !bytes.Equal(core, after) {
		t.Fatal("profile modified progress core")
	}
}

func TestMasterTitleRejectsInvalidRequestWithoutWriting(t *testing.T) {
	store := stateio.NewMemory()
	s, _ := newMasterProfile(t, store)
	before, _, _ := store.LoadEntry("progress", "master_title", "identity")
	for _, request := range [][]byte{nil, wire.AppendVarint(nil, 1, 0), wire.AppendString(wire.AppendVarint(nil, 1, 1), 2, ""), wire.AppendString(wire.AppendVarint(nil, 1, 1), 2, "Bad Name"), wire.AppendString(wire.AppendVarint(nil, 1, 1), 2, "ab"), wire.AppendVarint(wire.AppendVarint(wire.AppendString(wire.AppendVarint(nil, 1, 1), 2, "Player"), 3, 2), 4, 30)} {
		if _, _, handled, err := s.Handle("/MasterTitleInfoUpdate", request); !handled || err == nil {
			t.Fatal("invalid profile accepted")
		}
	}
	if _, _, _, err := s.Handle("/MasterTitleInfo", nil); err == nil {
		t.Fatal("missing info sequence accepted")
	}
	after, _, _ := store.LoadEntry("progress", "master_title", "identity")
	if !bytes.Equal(before, after) {
		t.Fatal("invalid profile request wrote state")
	}
}

type masterWriteFailure struct {
	*stateio.Memory
	fail bool
}

func (s *masterWriteFailure) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if s.fail {
		return errors.New("profile write failure")
	}
	return s.Memory.SaveWithEntries(domain, core, changes)
}

func TestMasterTitleWriteFailureLeavesPriorResponse(t *testing.T) {
	store := &masterWriteFailure{Memory: stateio.NewMemory()}
	s, _ := newMasterProfile(t, store)
	store.fail = true
	update := wire.AppendString(wire.AppendVarint(nil, 1, 1), 2, "ChangedName")
	if _, _, _, err := s.Handle("/MasterTitleInfoUpdate", update); err == nil {
		t.Fatal("write error ignored")
	}
	_, body, _, err := s.Handle("/MasterTitleInfo", wire.AppendVarint(nil, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	name, _, _ := wire.Bytes(body, 1)
	if string(name) != "Guest_30738202" {
		t.Fatal("failed write changed profile")
	}
}
