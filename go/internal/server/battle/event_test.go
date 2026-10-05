package battle

import (
	"bytes"
	"fmt"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

type fakeEventBattle struct {
	enter, end string
	rejected   bool
}

func (*fakeEventBattle) HandlesBattle(mode uint64) bool { return mode == 17 }
func (e *fakeEventBattle) EnterBattle(_ []byte, receipt string) ([]byte, error) {
	e.enter = receipt
	if e.rejected {
		return nil, fmt.Errorf("stage locked")
	}
	return wire.AppendVarint(nil, 1, 7), nil
}
func (e *fakeEventBattle) CompleteBattle(_ []byte, receipt string) ([]byte, error) {
	e.end = receipt
	return wire.AppendBytes(nil, 16, wire.AppendVarint(nil, 3, 77)), nil
}

func TestEventBattleOwnsSettlementAndAvoidsWorldDeck(t *testing.T) {
	s := NewService("unused", "unused", nil, func() (int, error) { return 21, nil })
	s.BeginSession("event-session")
	e := &fakeEventBattle{}
	s.AttachEventBattle(e)
	s.loadPhases = func(string, string, int, uint64, uint64) ([]gamedata.BattlePhase, error) {
		t.Fatal("event read ordinary world deck")
		return nil, nil
	}
	s.AttachCommittedHealth(func(map[uint64]uint64) error { t.Fatal("event altered world health"); return nil })
	enter := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(request(1), 3, 1), 4, 73), 5, 17)
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/BattleStart", wire.AppendVarint(request(2), 2, 73)); err != nil {
		t.Fatal(err)
	}
	code, out, _, err := s.Handle("/BattleEnd", wire.AppendVarint(request(3), 2, 1))
	if err != nil || code != 15 {
		t.Fatalf("settlement: %d %v", code, err)
	}
	if e.enter != "event-session:1" || e.end != e.enter {
		t.Fatalf("receipt %q/%q", e.enter, e.end)
	}
	row, _, _ := wire.Bytes(out, 16)
	if !bytes.Equal(row, wire.AppendVarint(nil, 3, 77)) {
		t.Fatal("event progress lost")
	}
	if s.Active() {
		t.Fatal("event battle remained active")
	}
	_, retry, _, err := s.Handle("/BattleEnd", wire.AppendVarint(request(3), 2, 1))
	if err != nil || !bytes.Equal(retry, out) {
		t.Fatal("settlement retry lost reply")
	}
	if _, _, _, err = s.Handle("/BattleEnd", wire.AppendVarint(request(3), 2, 2)); err == nil {
		t.Fatal("changed settlement retry accepted")
	}
	e.rejected = true
	if _, _, _, err = s.Handle("/BattleEnter", enter); err == nil {
		t.Fatal("locked stage entered")
	}
}
