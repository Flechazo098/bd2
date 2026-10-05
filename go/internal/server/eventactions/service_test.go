package eventactions

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
	"time"
)

type economyStub struct {
	calls          int
	costs, rewards []gamedata.Reward
}

func (e *economyStub) Apply(_ string, c, r []gamedata.Reward) ([]byte, error) {
	e.calls++
	e.costs = append(e.costs, c...)
	e.rewards = append(e.rewards, r...)
	return []byte{10, 0}, nil
}
func row(v map[int]uint64) gamedata.EventActionRow { return gamedata.EventActionRow{Values: v} }
func setup(t *testing.T) (*Service, *economyStub, stateio.Store) {
	t.Helper()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	r := events.NewRegistry()
	if e := r.Replace([]events.Schedule{{UID: 1, Type: 23, ID: 1, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}, {UID: 2, Type: 20, ID: 1, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}, {UID: 3, Type: 21, ID: 1, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}}); e != nil {
		t.Fatal(e)
	}
	d := &gamedata.EventActionsDesign{Tables: map[string][]gamedata.EventActionRow{"VotingEventTable": {row(map[int]uint64{6: 1, 10: 1, 14: 1, 7: 1, 8: 6001, 9: 8, 1: 1, 2: 6002, 3: 8})}, "VotingCandidateTable": {row(map[int]uint64{1: 1, 2: 9})}, "VotingRoundTable": {row(map[int]uint64{2: 1, 3: 1})}, "TacticsBingoGroupTable": {row(map[int]uint64{3: 1, 1: 5, 4: 2})}, "TacticsBingoTable": {row(map[int]uint64{4: 5, 5: 1, 2: 99})}, "FieldSpawnEventTable": {row(map[int]uint64{4: 1, 5: 1, 2: 1})}, "FieldEventMonsterTable": {row(map[int]uint64{3: 1, 4: 1, 1: 3001, 5: 3009})}, "FieldEventDefaultTable": {row(map[int]uint64{4: 10, 5: 2, 12: 40})}}, SpawnRewards: map[[2]uint64]gamedata.Reward{{3009, 3001}: {Type: 4, Count: 10000}}}
	economy := &economyStub{}
	d.Tables["FieldSpawnEventTable"][0].Text = map[int]string{7: "12:00:00", 1: "12:10:00"}
	store := stateio.NewMemory()
	s, e := Open(store, d, r, economy)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return now }
	s.BeginSession("test")
	return s, economy, store
}
func req(n uint64) []byte { return wire.AppendVarint(nil, 1, n) }
func TestVoteConsumesDesignCostRejectsRepeatAndExactReplay(t *testing.T) {
	s, e, store := setup(t)
	b := req(1)
	b = wire.AppendVarint(b, 2, 9)
	b = wire.AppendVarint(b, 4, 1)
	_, reply, _, err := s.Handle("/CharVoteSave", b)
	if err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || e.costs[0].ID != 6001 {
		t.Fatal("vote cost not applied")
	}
	next, err := Open(store, s.design, s.registry, e)
	if err != nil {
		t.Fatal(err)
	}
	next.now = s.now
	next.BeginSession("test")
	_, replay, _, err := next.Handle("/CharVoteSave", b)
	if err != nil || !bytes.Equal(reply, replay) || e.calls != 1 {
		t.Fatal("replay duplicated vote")
	}
	changed := wire.AppendVarint(b, 5, 1)
	if _, _, _, err = next.Handle("/CharVoteSave", changed); err == nil {
		t.Fatal("changed replay accepted")
	}
	b = wire.AppendVarint(req(2), 2, 9)
	b = wire.AppendVarint(b, 4, 1)
	if _, _, _, err = next.Handle("/CharVoteSave", b); err == nil {
		t.Fatal("second normal vote accepted")
	}
}
func TestTacticsBattleRequiresStageAndBindsWinReceipt(t *testing.T) {
	s, _, _ := setup(t)
	enter := req(1)
	enter = wire.AppendVarint(enter, 4, 99)
	enter = wire.AppendVarint(enter, 5, 29)
	enter = wire.AppendVarint(enter, 8, 5)
	enter = wire.AppendVarint(enter, 9, 1)
	if _, err := s.EnterBattle(enter, "enter1"); err != nil {
		t.Fatal(err)
	}
	end := wire.AppendVarint(req(2), 2, 1)
	reply, err := s.CompleteBattle(end, "end2")
	if err != nil || val(reply, 29) != 1 {
		t.Fatal("stage not cleared")
	}
	replay, err := s.CompleteBattle(end, "end2")
	if err != nil || !bytes.Equal(reply, replay) {
		t.Fatal("battle retry lost")
	}
	loss := wire.AppendVarint(req(2), 2, 2)
	if _, err = s.CompleteBattle(loss, "end2"); err == nil {
		t.Fatal("changed battle result accepted")
	}
}
func TestSpawnNeedsStartAndRejectsRecatch(t *testing.T) {
	s, e, _ := setup(t)
	reward := req(1)
	for f, v := range map[int]uint64{2: 1, 3: 1, 4: 1, 5: 1, 6: 3} {
		reward = wire.AppendVarint(reward, f, v)
	}
	if _, _, _, err := s.Handle("/FieldEventSpawnReward", reward); err == nil {
		t.Fatal("unstarted catch accepted")
	}
	start := wire.AppendVarint(req(2), 2, 3)
	start = wire.AppendVarint(start, 3, 1)
	start = wire.AppendVarint(start, 4, 1)
	if _, _, _, err := s.Handle("/FieldEventSpawnStart", start); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/FieldEventSpawnReward", reward); err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || s.state.DailyNormal != 1 {
		t.Fatal("catch not settled")
	}
}

func TestVotingNextRoundAdvancesRealTopCandidates(t *testing.T) {
	s, _, _ := setup(t)
	s.design.Tables["VotingCandidateTable"] = append(s.design.Tables["VotingCandidateTable"], row(map[int]uint64{1: 1, 2: 10}))
	s.design.Tables["VotingRoundTable"] = []gamedata.EventActionRow{row(map[int]uint64{2: 1, 3: 1, 1: 1}), row(map[int]uint64{2: 1, 3: 2, 4: 1})}
	s.state.Votes[key(1, 1, 9)] = &vote{Round: 1, Candidate: 9, Normal: 1}
	s.state.Votes[key(1, 1, 10)] = &vote{Round: 1, Candidate: 10, Additional: 2}
	v, r, _ := s.voteEvent()
	advanced := s.advancedCandidates(v, r, 2, s.candidates(r))
	if len(advanced) != 1 || advanced[0] != 10 {
		t.Fatalf("wrong advanced list %v", advanced)
	}
	s.state.Votes[key(1, 1, 9)].Additional = 1
	rows := s.rankRows(1, 1, s.candidates(r))
	if val(rows[0], 3) != 1 || val(rows[1], 3) != 1 {
		t.Fatal("equal votes did not share rank")
	}
}
