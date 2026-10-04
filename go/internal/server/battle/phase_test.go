package battle

import (
	"bytes"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func phaseService(t *testing.T) *Service {
	t.Helper()
	s := &Service{loadPhases: func(string, string, int, uint64, uint64) ([]gamedata.BattlePhase, error) {
		return []gamedata.BattlePhase{{GroupID: 70, ID: 101, DeckID: 11}, {GroupID: 70, ID: 205, DeckID: 12}, {GroupID: 70, ID: 309, DeckID: 13}}, nil
	}}
	enter := wire.AppendVarint(wire.AppendVarint(request(1), 4, 11), 5, 1)
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	return s
}

func phaseStart(t *testing.T, s *Service, seq, deck uint64, blue []byte) []byte {
	t.Helper()
	start := wire.AppendVarint(request(seq), 2, deck)
	start = wire.AppendBytes(start, 5, blue)
	code, reply, _, err := s.Handle("/BattleStart", start)
	if err != nil || code != 14 {
		t.Fatalf("start %d: code=%d err=%v", deck, code, err)
	}
	return reply
}

func TestPhaseLifecyclePreservesClientStateAndRejectsSkipping(t *testing.T) {
	s := phaseService(t)
	blue := wire.AppendVarint(wire.AppendVarint(nil, 2, 400), 4, 1234)
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(2)); err == nil {
		t.Fatal("phase advanced before start")
	}
	phaseStart(t, s, 3, 11, blue)
	commits := 0
	s.AttachCommittedHealth(func(map[uint64]uint64) error { commits++; return nil })
	win := wire.AppendVarint(request(4), 2, 1)
	if _, _, _, err := s.Handle("/BattleEnd", win); err == nil {
		t.Fatal("early victory accepted")
	}
	if commits != 0 {
		t.Fatal("early victory committed health")
	}
	code, reply, _, err := s.Handle("/BattlePhaseChange", request(5))
	if err != nil || code != 632 {
		t.Fatalf("phase response=%d %v", code, err)
	}
	for field, want := range map[int]uint64{1: 70, 2: 205, 8: 12} {
		got, found, err := wire.Varint(reply, field)
		if err != nil || !found || got != want {
			t.Fatalf("field %d = %d/%v: %v", field, got, found, err)
		}
	}
	if result, found, _ := wire.Bytes(reply, 3); found && len(result) != 0 {
		t.Fatal("fabricated verified battle result")
	}
	verify, _, _ := wire.Varint(reply, 7)
	if verify != 0 {
		t.Fatal("verification enabled without authoritative combat state")
	}
	_, replay, _, err := s.Handle("/BattlePhaseChange", request(5))
	if err != nil || !bytes.Equal(reply, replay) || s.stateLocked().phase != 1 {
		t.Fatal("replay advanced phase")
	}
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(6)); err == nil {
		t.Fatal("skipped unstarted phase")
	}
	if _, _, _, err := s.Handle("/BattleStart", wire.AppendVarint(request(7), 2, 13)); err == nil {
		t.Fatal("skipped deck accepted")
	}
	changedBlue := wire.AppendVarint(wire.AppendVarint(nil, 2, 400), 4, 600)
	response := phaseStart(t, s, 8, 12, changedBlue)
	got, _, _ := wire.Bytes(response, 2)
	if !bytes.Equal(got, changedBlue) || !bytes.Equal(s.stateLocked().initialBlue[0], blue) {
		t.Fatal("phase start reset player state or retry baseline")
	}
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(9)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/BattleEnd", win); err == nil {
		t.Fatal("victory before final start")
	}
	phaseStart(t, s, 10, 13, changedBlue)
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(11)); err == nil {
		t.Fatal("advanced past final phase")
	}
	if _, _, _, err := s.Handle("/BattleEnd", win); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(12)); err == nil {
		t.Fatal("inactive phase request accepted")
	}
}

func TestPhaseRetryRestoresFirstDeckAndInitialBlue(t *testing.T) {
	s := phaseService(t)
	blue := wire.AppendVarint(wire.AppendVarint(nil, 2, 401), 4, 200)
	phaseStart(t, s, 2, 11, blue)
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(3)); err != nil {
		t.Fatal(err)
	}
	phaseStart(t, s, 4, 12, wire.AppendVarint(nil, 4, 1))
	if _, _, _, err := s.Handle("/BattleRetry", wire.AppendVarint(request(5), 2, 11)); err == nil {
		t.Fatal("retry accepted wrong current deck")
	}
	code, response, _, err := s.Handle("/BattleRetry", wire.AppendVarint(request(6), 2, 12))
	index, _, _ := wire.Varint(response, 3)
	restored, _, _ := wire.Bytes(response, 2)
	if err != nil || code != 58 || index != 11 || !bytes.Equal(restored, blue) {
		t.Fatalf("retry: code=%d index=%d blue=%x err=%v", code, index, restored, err)
	}
	state := s.stateLocked()
	if state.phase != 0 || state.deck != 11 || state.phaseStarted || state.phaseReply != nil {
		t.Fatal("retry did not reset phase state")
	}
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(7)); err == nil {
		t.Fatal("retry advanced before start")
	}
}

func TestMalformedPhaseStartDoesNotConsumeRound(t *testing.T) {
	s := phaseService(t)
	start := append(wire.AppendVarint(request(2), 2, 11), 0x2a, 0x80)
	if _, _, _, err := s.Handle("/BattleStart", start); err == nil {
		t.Fatal("malformed start accepted")
	}
	if state := s.stateLocked(); state.round != 0 || state.index != 0 || state.phaseStarted {
		t.Fatal("malformed request mutated battle")
	}
}

func TestPhaseEntryRejectsLaterDeckWithoutReplacingActiveBattle(t *testing.T) {
	s := phaseService(t)
	phaseStart(t, s, 2, 11, wire.AppendVarint(nil, 2, 400))
	enter := wire.AppendVarint(wire.AppendVarint(request(3), 4, 12), 5, 1)
	if _, _, _, err := s.Handle("/BattleEnter", enter); err == nil {
		t.Fatal("entered a later phase directly")
	}
	state := s.stateLocked()
	if state.deck != 11 || state.index != 11 || !state.phaseStarted {
		t.Fatal("rejected enter replaced active battle")
	}
}

func TestOrdinaryBattleRejectsPhaseChange(t *testing.T) {
	s := &Service{}
	enter := wire.AppendVarint(wire.AppendVarint(request(1), 4, 11), 5, 1)
	if _, _, _, err := s.Handle("/BattleEnter", enter); err != nil {
		t.Fatal(err)
	}
	phaseStart(t, s, 2, 11, wire.AppendVarint(nil, 2, 400))
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(3)); err == nil {
		t.Fatal("ordinary battle accepted phase change")
	}
}

func TestPhaseVictoryRewardsOnlyFinalDeck(t *testing.T) {
	s := phaseService(t)
	var err error
	s.inventory, err = player.OpenInventory(stateio.NewMemory(), &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	s.gameDataRoot = "test-root"
	s.stateLocked().pack, s.stateLocked().monster = 77, 42
	loaded := uint64(0)
	s.loadRewards = func(_, _ string, pack int, deck uint64) ([]gamedata.BattleReward, error) {
		if pack != 77 {
			t.Fatalf("reward pack = %d", pack)
		}
		loaded = deck
		return []gamedata.BattleReward{{Type: 8, ID: 8, Count: 1}}, nil
	}
	blue := wire.AppendVarint(nil, 2, 400)
	phaseStart(t, s, 2, 11, blue)
	win := wire.AppendVarint(request(3), 2, 1)
	if _, _, _, err := s.Handle("/BattleEnd", win); err == nil {
		t.Fatal("early win accepted")
	}
	if loaded != 0 {
		t.Fatal("early win loaded rewards")
	}
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(4)); err != nil {
		t.Fatal(err)
	}
	phaseStart(t, s, 5, 12, blue)
	if _, _, _, err := s.Handle("/BattlePhaseChange", request(6)); err != nil {
		t.Fatal(err)
	}
	phaseStart(t, s, 7, 13, blue)
	if _, _, _, err := s.Handle("/BattleEnd", win); err != nil {
		t.Fatal(err)
	}
	if loaded != 13 || len(s.inventory.GrantedItems("pack77:monster42:deck13")) != 1 {
		t.Fatalf("final reward deck=%d", loaded)
	}
	if len(s.inventory.GrantedItems("pack77:monster42:deck11")) != 0 {
		t.Fatal("granted first phase reward")
	}
}
