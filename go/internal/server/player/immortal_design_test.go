package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
)

func TestImmortalRequiresDesignedDeadTalentAndReplaysSameSequence(t *testing.T) {
	store := stateio.NewMemory()
	inv, err := OpenInventory(store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 999, HP: 0, Level: 1, TalentLevel: 3}}, inv, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s.maxHealth = func(Character) (uint64, error) { return 700, nil }
	if err := s.AttachImmortalDesign(&gamedata.ImmortalDesign{Characters: map[uint64]uint64{999: 88}, FullRestore: map[[2]uint64]bool{{88, 3}: true}}); err != nil {
		t.Fatal(err)
	}
	s.BeginSession("a")
	req := wire.AppendVarint(wire.AppendVarint(nil, 1, 12), 2, 77)
	_, body, _, err := s.Handle("/CharImmortal", req)
	if err != nil {
		t.Fatal(err)
	}
	_, again, _, err := s.Handle("/CharImmortal", req)
	if err != nil || !bytes.Equal(body, again) {
		t.Fatalf("replay err=%v", err)
	}
	if _, _, _, err := s.Handle("/CharImmortal", wire.AppendVarint(wire.AppendVarint(nil, 1, 13), 2, 77)); err == nil {
		t.Fatal("alive new request restored")
	}
	if err := s.SetCurrentHealth(77, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachImmortalDesign(&gamedata.ImmortalDesign{Characters: map[uint64]uint64{999: 88}, FullRestore: map[[2]uint64]bool{{88, 2}: true}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/CharImmortal", wire.AppendVarint(wire.AppendVarint(nil, 1, 14), 2, 77)); err == nil {
		t.Fatal("wrong talent level revived")
	}
}
