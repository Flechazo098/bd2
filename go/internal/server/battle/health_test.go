package battle

import (
	"testing"

	"bd2server/internal/server/wire"
)

func TestBattleHealthCommitsOnlyAtEndAndChecksParticipants(t *testing.T) {
	s := &Service{}
	calls := 0
	var health map[uint64]uint64
	s.AttachCommittedHealth(func(values map[uint64]uint64) error {
		calls++
		for index, hp := range values {
			values[index] = min(hp, 50)
		}
		health = values
		return nil
	})
	s.BeginSession("first")
	enter := wire.AppendVarint(wire.AppendVarint(request(1), 4, 1), 5, 1)
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	start := wire.AppendVarint(request(2), 2, 11)
	blue := wire.AppendVarint(wire.AppendVarint(nil, 2, 100), 4, 60)
	start = wire.AppendBytes(start, 5, blue)
	if _, _, _, err := s.Handle("/BattleStart", start); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !s.Active() {
		t.Fatal("round state was committed or battle was not active")
	}
	invalid := wire.AppendVarint(request(3), 2, 1)
	invalid = wire.AppendBytes(invalid, 3, wire.AppendVarint(nil, 1, 999))
	if _, _, _, err := s.Handle("/BattleEnd", invalid); err == nil || calls != 0 {
		t.Fatal("accepted health for a character outside the blue team")
	}
	result := wire.AppendVarint(request(4), 2, 1)
	character := wire.AppendVarint(wire.AppendVarint(nil, 1, 100), 3, 17)
	result = wire.AppendBytes(result, 3, character)
	if _, _, _, err := s.Handle("/BattleEnd", result); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || health[100] != 17 || s.Active() {
		t.Fatalf("health=%v calls=%d active=%t", health, calls, s.Active())
	}
	// A second battle interrupted by a login never commits its round health.
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/BattleStart", start); err != nil {
		t.Fatal(err)
	}
	s.BeginSession("reconnected")
	if calls != 1 || s.Active() {
		t.Fatal("reconnect persisted unfinished battle health")
	}
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/BattleStart", start); err != nil {
		t.Fatal(err)
	}
	result = wire.AppendVarint(request(5), 2, 1)
	character = wire.AppendVarint(wire.AppendVarint(nil, 1, 100), 3, 999)
	result = wire.AppendBytes(result, 3, character)
	_, body, _, err := s.Handle("/BattleEnd", result)
	if err != nil {
		t.Fatal(err)
	}
	returned, _, _ := wire.Bytes(body, 3)
	hp, _, _ := wire.Varint(returned, 3)
	if health[100] != 50 || hp != 50 {
		t.Fatalf("committed and returned HP differ: saved=%v response=%d", health, hp)
	}
}
