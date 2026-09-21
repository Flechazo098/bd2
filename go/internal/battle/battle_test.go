package battle

import (
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

func request(seq uint64) []byte { return wire.AppendVarint(nil, 1, seq) }

func TestLocalBattleLifecycle(t *testing.T) {
	s := &Service{}
	enter := request(1)
	enter = wire.AppendVarint(enter, 4, 1)
	enter = wire.AppendVarint(enter, 5, 1)
	code, response, ok, err := s.Handle("/BattleEnter", enter)
	if err != nil || !ok || code != 52 {
		t.Fatalf("enter: %d %v %v", code, ok, err)
	}
	engine, found, _ := wire.Varint(response, 6)
	if !found || engine != 1 {
		t.Fatalf("engine=%d/%v", engine, found)
	}
	red := wire.AppendVarint(nil, 1, 1)
	blue := wire.AppendVarint(nil, 1, 6)
	start := request(2)
	start = wire.AppendVarint(start, 2, 1)
	start = wire.AppendBytes(start, 4, red)
	start = wire.AppendBytes(start, 5, blue)
	code, response, _, err = s.Handle("/BattleStart", start)
	if err != nil || code != 14 {
		t.Fatalf("start: %d %v", code, err)
	}
	if _, found, _ := wire.Bytes(response, 1); !found {
		t.Fatal("red state not echoed")
	}
	if _, found, _ := wire.Bytes(response, 2); !found {
		t.Fatal("blue state not echoed")
	}
	code, response, _, err = s.Handle("/BattleVerifyState", request(3))
	state, found, _ := wire.Varint(response, 1)
	if err != nil || code != 142 || !found || state != 3 {
		t.Fatalf("verify: code=%d state=%d/%v err=%v", code, state, found, err)
	}
	end := request(4)
	end = wire.AppendVarint(end, 2, 2)
	end = wire.AppendBytes(end, 3, blue)
	code, response, _, err = s.Handle("/BattleEnd", end)
	result, found, _ := wire.Varint(response, 1)
	if err != nil || code != 15 || !found || result != 2 {
		t.Fatalf("end: code=%d result=%d/%v err=%v", code, result, found, err)
	}
}

func TestBattleOrdering(t *testing.T) {
	s := &Service{}
	start := request(1)
	start = wire.AppendVarint(start, 2, 1)
	if _, _, _, err := s.Handle("/BattleStart", start); err == nil {
		t.Fatal("start without enter accepted")
	}
}

func TestBattleRetryRestoresFirstSubmittedBlueTeam(t *testing.T) {
	s := &Service{}
	enter := wire.AppendVarint(wire.AppendVarint(request(1), 4, 4), 5, 1)
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	first := wire.AppendVarint(wire.AppendVarint(nil, 2, 101), 4, 513)
	second := wire.AppendVarint(wire.AppendVarint(nil, 2, 102), 4, 200)
	start := wire.AppendVarint(request(2), 2, 77)
	start = wire.AppendBytes(start, 5, first)
	start = wire.AppendBytes(start, 5, second)
	if _, _, _, err := s.Handle("/BattleStart", start); err != nil {
		t.Fatal(err)
	}
	retry := wire.AppendVarint(request(3), 2, 77)
	code, response, handled, err := s.Handle("/BattleRetry", retry)
	if err != nil || !handled || code != 58 {
		t.Fatalf("retry code=%d handled=%v err=%v", code, handled, err)
	}
	index, found, err := wire.Varint(response, 3)
	if err != nil || !found || index != 77 {
		t.Fatalf("retry battle index=%d found=%v err=%v", index, found, err)
	}
	var restored [][]byte
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 2 {
			restored = append(restored, field.Value)
		}
		return nil
	}); err != nil || len(restored) != 2 || string(restored[0]) != string(first) || string(restored[1]) != string(second) {
		t.Fatalf("retry team=%x err=%v", restored, err)
	}
}

func TestBattleEnterUsesSamePictorialSnapshotAsAllCharRefresh(t *testing.T) {
	s := &Service{}
	s.AttachPictorialBuffs(func() ([]gamedata.PictorialBuffStat, error) {
		return []gamedata.PictorialBuffStat{{StatType: 2, Value: .0175}, {StatType: 4, Value: .01}}, nil
	})
	enter := wire.AppendVarint(wire.AppendVarint(request(9), 4, 1), 5, 1)
	code, response, handled, err := s.Handle("/BattleEnter", enter)
	if err != nil || !handled || code != 52 {
		t.Fatalf("battle entry code=%d handled=%v err=%v", code, handled, err)
	}
	var count int
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 4 {
			count++
			stat, found, err := wire.Varint(field.Value, 1)
			if err != nil || !found || (stat != 2 && stat != 4) {
				t.Fatalf("invalid battle buff stat=%d found=%v err=%v", stat, found, err)
			}
		}
		return nil
	}); err != nil || count != 2 {
		t.Fatalf("battle buffs count=%d err=%v", count, err)
	}
}

func TestBattleVictoryLocksPackAtEnterForRewardsAndIdentity(t *testing.T) {
	dir := t.TempDir()
	inventory, err := player.OpenInventory(filepath.Join(dir, "items.json"), &player.Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	currentPack := 22
	s := NewService("test-root", "test-version", inventory, func() (int, error) {
		return currentPack, nil
	})
	var loadedPack int
	var loadedDeck uint64
	s.loadRewards = func(_, _ string, packID int, deckID uint64) ([]gamedata.BattleReward, error) {
		loadedPack, loadedDeck = packID, deckID
		return []gamedata.BattleReward{{Type: 8, ID: 8, Count: 3}}, nil
	}

	enter := wire.AppendVarint(request(1), 3, 7)
	enter = wire.AppendVarint(enter, 4, 9)
	enter = wire.AppendVarint(enter, 5, 1)
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	// A later world transition must not change the identity of an in-flight
	// battle; the pack is captured at BattleEnter.
	currentPack = 21
	end := wire.AppendVarint(request(2), 2, 1)
	if _, _, _, err := s.Handle("/BattleEnd", end); err != nil {
		t.Fatal(err)
	}
	if loadedPack != 22 || loadedDeck != 9 {
		t.Fatalf("reward lookup pack/deck=%d/%d, want 22/9", loadedPack, loadedDeck)
	}
	if got := inventory.GrantedItems("pack22:monster7:deck9"); len(got) != 1 || got[0].ID != 8 || got[0].Count != 3 {
		t.Fatalf("pack22 reward grant=%+v", got)
	}
	if got := inventory.GrantedItems("pack21:monster7:deck9"); len(got) != 0 {
		t.Fatalf("reward leaked into pack21 identity: %+v", got)
	}
}
