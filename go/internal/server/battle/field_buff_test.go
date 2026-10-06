package battle

import (
	"bd2server/internal/server/wire"
	"errors"
	"testing"
)

func TestFieldBuffConsumptionAfterValidationAndBeforeBattleActivation(t *testing.T) {
	s := NewService("", "", nil, nil)
	s.BeginSession("first-session")
	remaining := 3
	used := map[string]bool{}
	fail := false
	s.AttachFieldBuffConsume(func(identity string) error {
		if fail {
			return errors.New("field buff persistence failed")
		}
		if !used[identity] {
			remaining--
			used[identity] = true
		}
		return nil
	})
	enter := wire.AppendVarint(wire.AppendVarint(request(1), 4, 1), 5, 1)
	if _, _, _, err := s.Handle("/BattleEnter", request(1)); err == nil || remaining != 3 {
		t.Fatal("invalid battle consumed buff")
	}
	fail = true
	if _, _, _, err := s.Handle("/BattleEnter", enter); err == nil || s.Active() {
		t.Fatal("persistence failure activated battle")
	}
	fail = false
	for range 2 {
		if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
			t.Fatal(err)
		}
	}
	if remaining != 2 {
		t.Fatal("same enter receipt consumed twice")
	}
	start := wire.AppendBytes(wire.AppendVarint(request(2), 2, 1), 5, wire.AppendVarint(nil, 1, 6))
	if _, _, _, err := s.Handle("/BattleStart", start); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/BattleRetry", wire.AppendVarint(request(3), 2, 1)); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatal("retry consumed extra field buff")
	}
	s.BeginSession("second-session")
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil || remaining != 1 {
		t.Fatal("new battle did not consume once", err)
	}
}
