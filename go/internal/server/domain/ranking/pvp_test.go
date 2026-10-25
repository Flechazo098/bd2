package ranking

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/matchmaking"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/shared"
)

func pvpTestEvent(t *testing.T, topic, id string, value any) command.Event {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return command.Event{ID: id, Topic: topic, Payload: payload}
}

// The final reward must include every accepted result even if another account
// already closed and delivery resumes only after the calculation deadline.
func TestPVPSeasonClosureWaitsForSourceBarrier(t *testing.T) {
	ctx := context.Background()
	store, err := shared.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	season := pvp.Season{Mode: pvp.Mirror, ID: 1, Start: 10, End: 20, CalculateEnd: 30}
	design := &gamedata.PvpDesign{Mirror: gamedata.PvpDefaults{StartVP: 1000, MinimumVP: 1000, HistoryLimit: 20}, MirrorSeasons: map[uint64]uint64{1: 1}, MirrorRanks: map[uint64][]gamedata.PvpRank{1: {{Group: 1, ID: 1, VP: 1000, SeasonRewards: []gamedata.BattleReward{{Type: 3, Count: 10}}}}}}
	source, err := NewPVP(ctx, testPVPStore{store}, design, []pvp.Season{season})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.BeginClosing(ctx, season, []string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	closed := pvp.SeasonClosed{Version: 1, OwnerID: "bob", Mode: pvp.Mirror, Season: 1}
	if err := source.Consume(ctx, "bob", pvpTestEvent(t, "pvp.season.closed", "closed-bob", closed)); err != nil {
		t.Fatal(err)
	}
	if err := source.FreezeDue(ctx, time.UnixMilli(50)); err != nil {
		t.Fatal(err)
	}
	if source.board(pvp.Mirror, 1).Frozen {
		t.Fatal("season froze before alice's earlier accepted result was drained")
	}
	battle := pvp.BattleSettled{Version: 1, ChallengeID: "challenge", BattleIndex: 1, Mode: pvp.Mirror, Season: 1, Result: 1, Multiplier: 1, Timestamp: 19, AttackerChange: 20, DefenderChange: -20,
		Attacker: pvp.PublishedSnapshot{Profile: pvp.Profile{OwnerID: "alice", OwnerIndex: 1}, Mode: pvp.Mirror, Season: 1, VP: 1000},
		Defender: pvp.PublishedSnapshot{Profile: pvp.Profile{OwnerID: "bob", OwnerIndex: 2}, Mode: pvp.Mirror, Season: 1, VP: 1000}}
	event := pvpTestEvent(t, "pvp.battle.settled", "pvp:result:challenge", battle)
	if err := source.Consume(ctx, "alice", event); err != nil {
		t.Fatal(err)
	}
	// A repeated committed challenge cannot award another win.
	if err := source.Consume(ctx, "alice", event); err != nil {
		t.Fatal(err)
	}
	closed.OwnerID = "alice"
	if err := source.Consume(ctx, "alice", pvpTestEvent(t, "pvp.season.closed", "closed-alice", closed)); err != nil {
		t.Fatal(err)
	}
	if err := source.FreezeDue(ctx, time.UnixMilli(50)); err != nil {
		t.Fatal(err)
	}
	// Reopening the projection must preserve both its barrier and final reward.
	source, err = NewPVP(ctx, testPVPStore{store}, design, []pvp.Season{season})
	if err != nil {
		t.Fatal(err)
	}
	personal, err := source.Personal(pvp.ProgressQuery{Mode: pvp.Mirror, Season: 1, Owner: "alice", PendingIDs: []string{"pvp:result:challenge"}})
	if err != nil {
		t.Fatal(err)
	}
	if personal.VP != 1020 || personal.AttackCount != 1 || personal.Wins != 1 || len(personal.AppliedIDs) != 1 {
		t.Fatalf("accepted challenge was not applied exactly once: %+v", personal)
	}
	rewards, err := source.Entitlements(pvp.Mirror, "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rewards) != 1 || rewards[0].VP != 1020 || rewards[0].AttackCount != 1 || rewards[0].Rank != 1 {
		t.Fatalf("final entitlement excludes delivered result: %+v", rewards)
	}
	rewards, err = source.Entitlements(pvp.Mirror, "bob", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rewards) != 0 {
		t.Fatalf("defense-only account received a season reward: %+v", rewards)
	}
}

func TestPVPCandidateIndexTracksScoreAndCopiesSnapshots(t *testing.T) {
	rater, err := newPVPRater()
	if err != nil {
		t.Fatal(err)
	}
	source := &PVP{design: &gamedata.PvpDesign{Mirror: gamedata.PvpDefaults{StartVP: 1000}}, boards: map[string]*pvpBoard{}, rater: rater, models: map[pvp.Mode]matchmaking.Model{pvp.Mirror: rater.NewModel()}}
	board := emptyPVPBoard()
	source.boards[pvpBoardKey(pvp.Mirror, 1)] = board
	for i, score := range []uint64{1040, 1000, 1010} {
		owner := fmt.Sprint(i)
		source.updateRow(board, owner, pvpStanding{Published: true, Defense: pvp.PublishedSnapshot{Profile: pvp.Profile{OwnerID: owner, OwnerIndex: uint64(i + 1)}, Mode: pvp.Mirror, Season: 1, Snapshot: pvp.FullSnapshot{FullDeck: []byte{1, 2}}}, Progress: pvp.PersonalProgress{Found: true, VP: score}})
	}
	row := board.Members["0"]
	row.Progress.VP = 1005
	source.updateRow(board, "0", row)
	candidates, err := source.Candidates(pvp.CandidateQuery{Mode: pvp.Mirror, Season: 1, Attacker: pvp.PublishedSnapshot{Profile: pvp.Profile{OwnerID: "2"}, VP: 1000}, Exclude: []string{"1"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Profile.OwnerID != "0" || candidates[0].VP != 1005 || candidates[0].Rank != 2 {
		t.Fatalf("updated opponent index wrong: %+v", candidates)
	}
	candidates[0].Snapshot.FullDeck[0] = 255
	if board.Members["0"].Defense.Snapshot.FullDeck[0] != 1 {
		t.Fatal("source leaked mutable snapshot storage")
	}
}

// Opponents are chosen by the predicted evenness of the contest, not by the
// nearest season score, and every accepted battle rates both sides durably.
func TestPVPMatchingFollowsSkillRatings(t *testing.T) {
	ctx := context.Background()
	store, err := shared.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	season := pvp.Season{Mode: pvp.Mirror, ID: 1, Start: 10, End: 1 << 50, CalculateEnd: 1 << 51}
	design := &gamedata.PvpDesign{Mirror: gamedata.PvpDefaults{StartVP: 1000, MinimumVP: 1000, HistoryLimit: 20}}
	source, err := NewPVP(ctx, testPVPStore{store}, design, []pvp.Season{season})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func(owner string, index uint64) pvp.PublishedSnapshot {
		return pvp.PublishedSnapshot{Profile: pvp.Profile{OwnerID: owner, OwnerIndex: index}, Mode: pvp.Mirror, Season: 1, Version: 1, VP: 1000, Snapshot: pvp.FullSnapshot{FullDeck: wire.AppendBytes(nil, 1, []byte{8, 1})}}
	}
	for index, owner := range []string{"alice", "novice", "expert"} {
		if err := source.Consume(ctx, owner, pvpTestEvent(t, "pvp.defense.published", "defense-"+owner, pvp.DefensePublished{Version: 1, Defense: snapshot(owner, uint64(index+1))})); err != nil {
			t.Fatal(err)
		}
	}
	// The expert already climbed the season; the novice shares alice's score.
	row := source.board(pvp.Mirror, 1).Members["expert"]
	row.Progress = pvp.PersonalProgress{Found: true, VP: 1300}
	source.updateRow(source.board(pvp.Mirror, 1), "expert", row)
	source.ratings[pvp.Mirror]["alice"] = matchmaking.Rating{Mu: 1.5, Phi: 0.3, Sigma: 0.06}
	source.ratings[pvp.Mirror]["expert"] = matchmaking.Rating{Mu: 1.5, Phi: 0.3, Sigma: 0.06}
	source.ratings[pvp.Mirror]["novice"] = matchmaking.Rating{Mu: -1.5, Phi: 0.3, Sigma: 0.06}
	candidates, err := source.Candidates(pvp.CandidateQuery{Mode: pvp.Mirror, Season: 1, FirstAttack: 1, At: 100, Attacker: snapshot("alice", 1), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Profile.OwnerID != "expert" {
		t.Fatalf("matched %+v, want the equally skilled expert", candidates)
	}

	before := source.rating(pvp.Mirror, "novice")
	battle := pvp.BattleSettled{Version: 1, ChallengeID: "upset", BattleIndex: 1, Mode: pvp.Mirror, Season: 1, Result: 2, Multiplier: 1, FirstAttack: 1, Timestamp: 100, Attacker: snapshot("alice", 1), Defender: snapshot("novice", 2)}
	event := pvpTestEvent(t, "pvp.battle.settled", "pvp:result:upset", battle)
	if err := source.Consume(ctx, "alice", event); err != nil {
		t.Fatal(err)
	}
	if live := source.rating(pvp.Mirror, "novice"); live.Games != 1 || live.Mu <= before.Mu {
		t.Fatalf("live matching still sees the pre-battle rating %+v", live)
	}
	source, err = NewPVP(ctx, testPVPStore{store}, design, []pvp.Season{season})
	if err != nil {
		t.Fatal(err)
	}
	alice, novice := source.rating(pvp.Mirror, "alice"), source.rating(pvp.Mirror, "novice")
	if alice.Games != 1 || novice.Games != 1 || novice.Mu <= before.Mu || source.models[pvp.Mirror].Observations != 1 {
		t.Fatalf("defending upset not rated durably: alice %+v novice %+v (was %+v) model %+v", alice, novice, before, source.models[pvp.Mirror])
	}
	model := source.models[pvp.Mirror]
	model.Player = append([]float64(nil), model.Player...)
	model.Contest = append([]float64(nil), model.Contest...)
	if err := source.Consume(ctx, "alice", event); err != nil {
		t.Fatal(err)
	}
	source, err = NewPVP(ctx, testPVPStore{store}, design, []pvp.Season{season})
	if err != nil {
		t.Fatal(err)
	}
	if source.rating(pvp.Mirror, "alice") != alice || source.rating(pvp.Mirror, "novice") != novice || !reflect.DeepEqual(source.models[pvp.Mirror], model) {
		t.Fatal("replayed settlement changed durable ratings or the learned model")
	}

}

type testPVPStore struct{ *shared.Store }
type testPVPTx struct{ *shared.Tx }

func (s testPVPStore) View(ctx context.Context, fn func(Tx) error) error {
	return s.Store.View(ctx, func(tx *shared.Tx) error { return fn(testPVPTx{tx}) })
}
func (s testPVPStore) Update(ctx context.Context, fn func(Tx) error) error {
	return s.Store.Update(ctx, func(tx *shared.Tx) error { return fn(testPVPTx{tx}) })
}
func (t testPVPTx) Scan(bucket, prefix, after string, limit int) ([]Entry, error) {
	rows, e := t.Tx.Scan(bucket, prefix, after, limit)
	if e != nil {
		return nil, e
	}
	out := make([]Entry, len(rows))
	for i, row := range rows {
		out[i] = Entry{row.Key, row.Value}
	}
	return out, nil
}
