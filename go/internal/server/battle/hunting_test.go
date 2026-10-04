package battle

import (
	"errors"
	"testing"

	"bd2server/internal/server/wire"
)

type huntingFixture struct {
	validationError     error
	settlementError     error
	pack                int
	mode, monster, deck uint64
	receipt             string
	settlements         int
}

func (h *huntingFixture) ValidateBattle(pack int, mode, monster, deck uint64) error {
	h.pack, h.mode, h.monster, h.deck = pack, mode, monster, deck
	return h.validationError
}
func (h *huntingFixture) CompleteBattle(pack int, mode, monster, deck uint64, receipt string) ([]byte, [][]byte, error) {
	h.pack, h.mode, h.monster, h.deck, h.receipt = pack, mode, monster, deck, receipt
	h.settlements++
	return wire.AppendVarint(nil, 2, 7), [][]byte{wire.AppendVarint(nil, 1, monster)}, h.settlementError
}

func TestHuntingBattleUsesLockedEncounterAndSessionReceipt(t *testing.T) {
	pack := 1
	s := NewService("", "", nil, func() (int, error) { return pack, nil })
	h := &huntingFixture{}
	s.AttachHunting(h)
	s.BeginSession("login-A")
	enter := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(request(1), 3, 11), 4, 12), 5, huntingGroundMode)
	h.validationError = errors.New("locked difficulty")
	if _, _, _, err := s.Handle("/BattleEnter", enter); err == nil || s.Active() {
		t.Fatal("invalid encounter accepted")
	}
	h.validationError = nil
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	pack = 2
	h.settlementError = errors.New("not enough AP")
	end := wire.AppendVarint(request(2), 2, 1)
	if _, _, _, err := s.Handle("/BattleEnd", end); err == nil || !s.Active() {
		t.Fatal("failed settlement ended battle")
	}
	h.settlementError = nil
	code, response, _, err := s.Handle("/BattleEnd", end)
	if err != nil || code != 15 {
		t.Fatalf("settle: %d %v", code, err)
	}
	if h.pack != 1 || h.mode != huntingGroundMode || h.monster != 11 || h.deck != 12 || h.receipt != "login-A:2" {
		t.Fatalf("settled wrong encounter: %+v", h)
	}
	if _, found, _ := wire.Bytes(response, 4); !found {
		t.Fatal("missing monster progress")
	}
	if bundle, found, _ := wire.Bytes(response, 5); !found || len(bundle) == 0 {
		t.Fatal("missing hunting reward")
	}
	if _, _, _, err := s.Handle("/BattleEnd", end); err == nil {
		t.Fatal("completed battle settled twice")
	}
}
