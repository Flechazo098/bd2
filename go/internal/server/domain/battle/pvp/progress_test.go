package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"testing"
)

type progressSource struct {
	Source
	value PersonalProgress
}

func (s *progressSource) Personal(ProgressQuery) (PersonalProgress, error) { return s.value, nil }

// Local receipts bridge durable player commits and asynchronous shared
// projection. Acknowledged wins must disappear from that bridge exactly once.
func TestPendingProgressReconcilesAcknowledgedResults(t *testing.T) {
	source := &progressSource{}
	service := &Service{rules: &Rules{Design: &gamedata.PvpDesign{Mirror: gamedata.PvpDefaults{StartVP: 1000, MinimumVP: 1000}}}, profile: Profile{OwnerID: "alice"}, source: source, state: state{Modes: map[Mode]*modeState{Mirror: {Progress: map[string]progress{}, Pending: map[string]pendingResult{"win": {Season: 1, Order: 1, Delta: 20, Result: 1}, "loss": {Season: 1, Order: 2, Delta: -30, Result: 2}}}}}}
	before, err := service.progress(Mirror, 1)
	if err != nil {
		t.Fatal(err)
	}
	if before.VP != 1000 || before.Attacks != 2 || before.Wins != 1 || before.Losses != 1 {
		t.Fatalf("pending results lost ordered floor semantics: %+v", before)
	}
	source.value = PersonalProgress{Found: true, VP: 1020, AttackCount: 1, Wins: 1, AppliedIDs: []string{"win"}}
	midway, err := service.progress(Mirror, 1)
	if err != nil {
		t.Fatal(err)
	}
	if midway.VP != before.VP || midway.Attacks != before.Attacks || midway.Wins != before.Wins || midway.Losses != before.Losses {
		t.Fatalf("partially projected receipt applied twice: before=%+v after=%+v", before, midway)
	}
	source.value = PersonalProgress{Found: true, VP: 1000, AttackCount: 2, Wins: 1, Losses: 1, AppliedIDs: []string{"win", "loss"}}
	after, err := service.progress(Mirror, 1)
	if err != nil {
		t.Fatal(err)
	}
	if after.VP != before.VP || after.Attacks != 2 || after.Wins != 1 || after.Losses != 1 {
		t.Fatalf("fully projected receipts changed progress: %+v", after)
	}
}
