package battle

import (
	"errors"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

type monsterHuntFixture struct {
	enter, end               []byte
	enterReceipt, endReceipt string
	err                      error
}

func (h *monsterHuntFixture) EnterBattle(request []byte, receipt string) ([]byte, error) {
	h.enter, h.enterReceipt = append([]byte(nil), request...), receipt
	return wire.AppendBytes(nil, 5, wire.AppendVarint(nil, 1, 11)), h.err
}
func (h *monsterHuntFixture) CompleteBattle(request []byte, receipt string) ([]byte, error) {
	h.end, h.endReceipt = append([]byte(nil), request...), receipt
	return wire.AppendBytes(nil, 13, wire.AppendVarint(nil, 1, 11)), h.err
}

func TestMonsterHuntBattleUsesSpecialRuntimeAndLeavesFieldHealthAlone(t *testing.T) {
	for _, mode := range []uint64{8, 24} {
		s := NewService("", "", nil, func() (int, error) { return 1, nil })
		h := &monsterHuntFixture{}
		s.AttachMonsterHunt(h)
		s.BeginSession("login-A")
		s.loadPhases = func(string, string, int, uint64, uint64) ([]gamedata.BattlePhase, error) {
			t.Fatal("monster hunt used pack phases")
			return nil, nil
		}
		s.AttachCommittedHealth(func(map[uint64]uint64) error { t.Fatal("monster hunt changed field HP"); return nil })
		s.AttachMonsterWinMission(func() error { t.Fatal("monster hunt awarded field monster mission"); return nil })
		s.AttachTutorialWin(func() error { t.Fatal("monster hunt awarded tutorial progress"); return nil })
		enter := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(request(1), 4, 12), 5, mode), 6, 91)
		h.err = errors.New("hunt unavailable")
		if _, _, _, err := s.Handle("/BattleEnter", enter); err == nil || s.Active() {
			t.Fatal("rejected hunt entered")
		}
		h.err = nil
		code, response, _, err := s.Handle("/BattleEnter", enter)
		if err != nil || code != 52 {
			t.Fatalf("enter: %d %v", code, err)
		}
		if _, found, _ := wire.Bytes(response, 5); !found {
			t.Fatal("missing hunt user info at enter")
		}
		end := wire.AppendVarint(wire.AppendVarint(request(2), 2, 1), 7, 1234)
		h.err = errors.New("invalid hunt settlement")
		if _, _, _, err := s.Handle("/BattleEnd", end); err == nil || !s.Active() {
			t.Fatal("failed hunt settlement finished battle")
		}
		h.err = nil
		code, response, _, err = s.Handle("/BattleEnd", end)
		if err != nil || code != 15 || h.enterReceipt != "login-A:1" || h.endReceipt != h.enterReceipt {
			t.Fatalf("end: %d %v %+v", code, err, h)
		}
		if _, found, _ := wire.Bytes(response, 13); !found {
			t.Fatal("missing hunt settlement progress")
		}
		if _, found, _ := wire.Bytes(response, 5); found {
			t.Fatal("hunt used ordinary pack reward bundle")
		}
		if s.Active() {
			t.Fatal("hunt battle still active")
		}
	}
}
