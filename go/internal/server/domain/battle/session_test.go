package battle

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/base64"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func packet(seq uint64) []byte { return wire.AppendVarint(nil, 1, seq) }

func enter(t *testing.T, s *Service, ctx command.Context) {
	t.Helper()
	req := wire.AppendVarint(packet(1), 4, 80)
	req = wire.AppendVarint(req, 5, 2)
	if _, _, handled, err := s.Handle(ctx, "/BattleEnter", req); !handled || err != nil {
		t.Fatalf("enter: %v", err)
	}
}

func startPacket(seq, turn, hp, sp uint64, blue bool) ([]byte, []byte) {
	char := wire.AppendVarint(nil, 1, 7)
	char = wire.AppendVarint(char, 2, 9001)
	char = wire.AppendVarint(char, 3, 300)
	char = wire.AppendVarint(char, 4, hp)
	char = wire.AppendBytes(char, 13, wire.AppendVarint(nil, 1, turn+100))
	result := wire.AppendBytes(nil, 3, char)
	result = wire.AppendBytes(result, 2, wire.AppendVarint(nil, 3, 500))
	result = wire.AppendBytes(result, 4, wire.AppendVarint(nil, 3, turn+600))
	result = wire.AppendBytes(result, 5, wire.AppendVarint(nil, 3, turn))
	common := wire.AppendVarint(nil, 1, 2)
	common = wire.AppendVarint(common, 2, 80)
	common = wire.AppendVarint(common, 3, turn-1)
	checkpoint := wire.AppendBytes(nil, 1, common)
	checkpoint = wire.AppendString(checkpoint, 8, base64.StdEncoding.EncodeToString(result))
	checkpoint = wire.AppendVarint(checkpoint, 9, sp)
	checkpoint = wire.AppendVarint(checkpoint, 10, 13)
	checkpoint = wire.AppendVarint(checkpoint, 28, turn+20)
	checkpoint = wire.AppendBytes(checkpoint, 29, wire.AppendVarint(nil, 1, turn+700))
	checkpoint = wire.AppendVarint(checkpoint, 101, boolNumber(blue))
	req := wire.AppendVarint(packet(seq), 2, 80)
	deck := wire.AppendVarint(nil, 1, 9001)
	deck = wire.AppendVarint(deck, 2, 5)
	deck = wire.AppendVarint(deck, 3, 1)
	req = wire.AppendBytes(req, 3, deck)
	req = wire.AppendBytes(req, 5, char)
	req = wire.AppendBytes(req, 100, checkpoint)
	return req, result
}

func play(t *testing.T, s *Service, ctx command.Context, seq, turn, hp, sp uint64, blue bool) []byte {
	t.Helper()
	req, result := startPacket(seq, turn, hp, sp, blue)
	if _, _, _, err := s.Handle(ctx, "/BattleStart", req); err != nil {
		t.Fatalf("start turn %d: %v", turn, err)
	}
	return result
}

func TestCheckpointReopenResumesAndRetriesPreviousBlueTurn(t *testing.T) {
	ctx := command.Context{Identity: command.Identity{AccountID: "a", SessionID: "first"}, State: stateio.NewMemory()}
	s := NewService("", "", nil)
	enter(t, s, ctx)
	first := play(t, s, ctx, 2, 1, 100, 5, true)
	play(t, s, ctx, 3, 2, 80, 7, false)
	current := play(t, s, ctx, 4, 3, 60, 9, true)
	ctx.SessionID = "reconnect"
	s = NewService("", "", nil)
	_, body, _, err := s.Handle(ctx, "/BattleResume", packet(1))
	if err != nil {
		t.Fatal(err)
	}
	if got := one(body, 8); string(got) != base64.StdEncoding.EncodeToString(current) || value(body, 9) != 9 || value(body, 28) != 23 {
		t.Fatalf("resume lost combat state: %x", body)
	}
	if value(one(body, 1), 3) != 2 || value(body, 27) != 1 {
		t.Fatalf("resume turn/retry: %x", body)
	}
	_, body, _, err = s.Handle(ctx, "/BattleRetryPreviousTurn", wire.AppendVarint(packet(2), 2, 80))
	if err != nil {
		t.Fatal(err)
	}
	if string(one(body, 1)) != base64.StdEncoding.EncodeToString(first) || value(body, 2) != 5 || value(body, 9) != 21 || value(one(body, 10), 1) != 701 || value(body, 7) != 0 {
		t.Fatalf("retry did not restore previous BLUE boundary: %x", body)
	}
	play(t, s, ctx, 3, 1, 100, 5, true)
	_, _, _, err = s.Handle(ctx, "/BattleGiveUp", packet(4))
	if err != nil {
		t.Fatal(err)
	}
	s = NewService("", "", nil)
	_, body, _, err = s.Handle(ctx, "/BattleResume", packet(5))
	if err != nil || len(body) != 0 {
		t.Fatalf("abandoned battle survived reopening: %x %v", body, err)
	}
}

func TestRetryOnRedTurnRestoresTheLastCompletedBlueTurn(t *testing.T) {
	ctx := command.Context{Identity: command.Identity{SessionID: "s"}, State: stateio.NewMemory()}
	s := NewService("", "", nil)
	enter(t, s, ctx)
	play(t, s, ctx, 2, 1, 100, 5, true)
	play(t, s, ctx, 3, 2, 80, 7, false)
	blue := play(t, s, ctx, 4, 3, 60, 9, true)
	play(t, s, ctx, 5, 4, 40, 11, false)
	_, body, _, err := s.Handle(ctx, "/BattleRetryPreviousTurn", wire.AppendVarint(packet(6), 2, 80))
	if err != nil || string(one(body, 1)) != base64.StdEncoding.EncodeToString(blue) || value(body, 7) != 2 {
		t.Fatalf("red turn retry selected wrong blue turn: %x %v", body, err)
	}
}

func TestResumeRepeatsTheSavedTurnAndStartRetriesDoNotAdvance(t *testing.T) {
	ctx := command.Context{Identity: command.Identity{SessionID: "s"}, State: stateio.NewMemory()}
	s := NewService("", "", nil)
	enter(t, s, ctx)
	req, _ := startPacket(2, 1, 100, 5, true)
	_, first, _, err := s.Handle(ctx, "/BattleStart", req)
	if err != nil {
		t.Fatal(err)
	}
	_, repeated, _, err := s.Handle(ctx, "/BattleStart", req)
	if err != nil || !bytes.Equal(first, repeated) {
		t.Fatalf("start retry changed seed: %x %x %v", first, repeated, err)
	}
	ctx.SessionID = "new"
	s = NewService("", "", nil)
	if _, _, _, err = s.Handle(ctx, "/BattleResume", packet(1)); err != nil {
		t.Fatal(err)
	}
	req, _ = startPacket(2, 1, 100, 5, true)
	_, resumed, _, err := s.Handle(ctx, "/BattleStart", req)
	if err != nil || !bytes.Equal(first, resumed) {
		t.Fatalf("resume skipped saved turn or changed random stream: %x %x %v", first, resumed, err)
	}
}

type failingBattleStore struct {
	stateio.AtomicEntryStore
	fail bool
}

func (s *failingBattleStore) PutEntry(domain, bucket, key string, raw []byte) error {
	if s.fail && domain == "battle" && bucket == "session" {
		return errors.New("disk unavailable")
	}
	return s.AtomicEntryStore.PutEntry(domain, bucket, key, raw)
}

func TestCheckpointFailurePreservesDurableBattleAndCompletionIsOnce(t *testing.T) {
	store := &failingBattleStore{AtomicEntryStore: stateio.NewMemory()}
	ctx := command.Context{Identity: command.Identity{SessionID: "s"}, State: store}
	s := NewService("", "", nil)
	enter(t, s, ctx)
	play(t, s, ctx, 2, 1, 100, 5, true)
	store.fail = true
	req, _ := startPacket(3, 2, 80, 7, false)
	if _, _, _, err := s.Handle(ctx, "/BattleStart", req); err == nil {
		t.Fatal("storage failure accepted")
	}
	store.fail = false
	if _, _, _, err := s.Handle(ctx, "/BattleStart", req); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.AttachCompletion(func(_ command.Context, c Completion) ([]byte, error) {
		calls++
		if !c.Win || len(c.Participants) != 1 || c.Participants[0] != 9001 || len(c.Deck) != 1 || c.Deck[0].CostumeInvenIndex != 5 {
			t.Fatalf("wrong actual participants: %+v", c)
		}
		return wire.AppendBytes(nil, 9, []byte{8, 1}), nil
	})
	end := wire.AppendVarint(packet(4), 2, 1)
	_, first, _, err := s.Handle(ctx, "/BattleEnd", end)
	if err != nil {
		t.Fatal(err)
	}
	_, retry, _, err := s.Handle(ctx, "/BattleEnd", end)
	if err != nil || calls != 1 || !bytes.Equal(first, retry) {
		t.Fatalf("settlement applied twice: calls=%d %v", calls, err)
	}
}

func TestStartupRestoreBindsActualLoginBeforeBattleRequests(t *testing.T) {
	ctx := command.Context{Identity: command.Identity{SessionID: "old"}, State: stateio.NewMemory()}
	s := NewService("", "", nil)
	enter(t, s, ctx)
	play(t, s, ctx, 2, 1, 100, 5, true)
	s = NewService("", "", nil)
	ctx.SessionID = "startup"
	if err := s.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	ctx.SessionID = "actual-login"
	s.BeginLogin(ctx)
	if !s.Active(ctx) || s.RoundCount(ctx) != 1 {
		t.Fatal("a persisted active encounter was hidden from field/quest guards after login")
	}
}

func TestRecordedActionsAndBestStatisticsPersistForBothBossModes(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../data/resources/GameData"))
	ctx := command.Context{Identity: command.Identity{SessionID: "s"}, State: stateio.NewMemory()}
	for _, scenario := range []struct{ mode, monster, team uint64 }{{8, 10001, 1}, {23, 97000, 0}} {
		s := NewService(root, "20261008155037", nil)
		char := wire.AppendVarint(nil, 1, 300)
		team := wire.AppendBytes(wire.AppendVarint(nil, 1, scenario.team), 2, char)
		history := wire.AppendBytes(nil, 1, team)
		action := wire.AppendVarint(nil, 1, 301)
		action = wire.AppendVarint(action, 2, 1)
		action = wire.AppendVarint(action, 3, 1)
		action = wire.AppendVarint(action, 4, 5)
		action = wire.AppendVarint(action, 5, 1)
		action = wire.AppendVarint(action, 6, scenario.team)
		characterAction := wire.AppendBytes(wire.AppendVarint(nil, 1, 300), 5, action)
		characterRecord := wire.AppendBytes(nil, 1, characterAction)
		record := wire.AppendBytes(wire.AppendVarint(nil, 1, 300), 5, characterRecord)
		turn := wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 2, record)
		history = wire.AppendBytes(history, 2, wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 2, turn))
		req := wire.AppendVarint(packet(1), 2, scenario.mode)
		req = wire.AppendVarint(req, 3, scenario.monster)
		req = wire.AppendBytes(req, 4, history)
		req = wire.AppendVarint(req, 5, 900)
		if _, _, _, err := s.Handle(ctx, "/BattleRecordSave", req); err != nil {
			t.Fatal(err)
		}
		s = NewService(root, "20261008155037", nil)
		query := wire.AppendVarint(wire.AppendVarint(packet(2), 2, scenario.mode), 3, scenario.monster)
		_, body, _, err := s.Handle(ctx, "/BattleRecordInfo", query)
		if err != nil || value(body, 4) != 900 || !bytes.Equal(one(body, 3), history) {
			t.Fatalf("boss record lost on reopen: %x %v", body, err)
		}
		damage := wire.AppendVarint(wire.AppendVarint(nil, 1, 300), 2, 900)
		stats := wire.AppendBytes(nil, 1, damage)
		if err = s.saveStatistics(ctx, scenario.mode, stats); err != nil {
			t.Fatal(err)
		}
		if err = s.saveStatistics(ctx, scenario.mode, wire.AppendBytes(nil, 1, wire.AppendVarint(wire.AppendVarint(nil, 1, 300), 2, 100))); err != nil {
			t.Fatal(err)
		}
		s = NewService(root, "20261008155037", nil)
		_, body, _, err = s.Handle(ctx, "/BattleStatisticsInfo", wire.AppendVarint(packet(3), 2, scenario.mode))
		if err != nil || !bytes.Equal(one(body, 1), stats) {
			t.Fatalf("a lower damage run erased best statistics: %x %v", body, err)
		}
	}
}

func TestBattleEndKeepsBestDamageStatisticsAcrossEncounters(t *testing.T) {
	ctx := command.Context{Identity: command.Identity{SessionID: "statistics"}, State: stateio.NewMemory()}
	var best []byte
	for i, damage := range []uint64{900, 100} {
		s := NewService("", "", nil)
		seq := uint64(i*3 + 1)
		entry := wire.AppendVarint(wire.AppendVarint(packet(seq), 4, 80), 5, 2)
		if _, _, _, err := s.Handle(ctx, "/BattleEnter", entry); err != nil {
			t.Fatal(err)
		}
		start, _ := startPacket(seq+1, 1, 100, 5, true)
		if _, _, _, err := s.Handle(ctx, "/BattleStart", start); err != nil {
			t.Fatal(err)
		}
		stats := wire.AppendBytes(nil, 1, wire.AppendVarint(wire.AppendVarint(nil, 1, 300), 2, damage))
		end := wire.AppendBytes(wire.AppendVarint(packet(seq+2), 2, 1), 10, stats)
		if _, _, _, err := s.Handle(ctx, "/BattleEnd", end); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			best = stats
		}
		s = NewService("", "", nil)
		_, body, _, err := s.Handle(ctx, "/BattleStatisticsInfo", wire.AppendVarint(packet(99), 2, 2))
		if err != nil || !bytes.Equal(one(body, 1), best) {
			t.Fatalf("settled statistics lost across encounters: %x %v", body, err)
		}
	}
}
