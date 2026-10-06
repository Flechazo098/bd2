package eventactions

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type trackerTransactionEconomy struct{ store stateio.Store }

func (e trackerTransactionEconomy) Apply(_ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error) {
	return nil, e.store.Save("tracker-reward", []byte(`{"Granted":true}`))
}

func TestTrackerRewardAndProgressFailureRollbackTogether(t *testing.T) {
	s, _, _ := setup(t)
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.store = repo
	s.economy = trackerTransactionEconomy{repo}
	if _, _, _, err = s.Handle("/FieldEventSpawnStart", spawnStart(1, 1)); err != nil {
		t.Fatal(err)
	}
	s.AttachProgress(func(uint64, uint64, uint64) error { return errors.New("mission write failed") })
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/FieldEventSpawnReward", spawnCatch(2)); err == nil {
		t.Fatal("mission failure was ignored")
	}
	if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	b, err := repo.Load("tracker-reward")
	if err != nil || b != nil {
		t.Fatal("reward survived transaction rollback")
	}
	next, err := Open(repo, s.design, s.registry, trackerTransactionEconomy{repo})
	if err != nil {
		t.Fatal(err)
	}
	next.now = s.now
	next.BeginSession("retry")
	if next.state.DailyNormal != 0 || len(next.state.Spawns[3].Caught) != 0 {
		t.Fatal("catch survived failed mission transaction")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = next.Handle("/FieldEventSpawnReward", spawnCatch(3)); err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	b, err = repo.Load("tracker-reward")
	if err != nil || b == nil || next.state.DailyNormal != 1 {
		t.Fatal("retry failed to commit reward and catch together")
	}
}

func spawnStart(seq, id uint64) []byte {
	b := wire.AppendVarint(req(seq), 2, 3)
	b = wire.AppendVarint(b, 3, 1)
	return wire.AppendVarint(b, 4, id)
}
func spawnCatch(seq uint64) []byte {
	b := req(seq)
	for _, f := range [][2]uint64{{2, 1}, {3, 1}, {4, 1}, {5, 1}, {6, 3}} {
		b = wire.AppendVarint(b, int(f[0]), f[1])
	}
	return b
}

func TestTrackerPrepareCatchResumeAndCrossSequenceRetry(t *testing.T) {
	s, e, store := setup(t)
	info := wire.AppendVarint(req(1), 2, 3)
	_, b, _, err := s.Handle("/FieldEventSpawnInfo", info)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, _ := wire.Bytes(b, 1); exists {
		t.Fatal("new player received fabricated non-null progress")
	}
	if _, _, _, err = s.Handle("/FieldEventSpawnStart", spawnStart(2, 1)); err != nil {
		t.Fatal(err)
	}
	progressCalls := 0
	s.AttachProgress(func(kind, target, count uint64) error {
		if kind != 349 || target != 0 || count != 1 {
			t.Fatal("wrong catch mission progress")
		}
		progressCalls++
		return nil
	})
	if _, _, _, err = s.Handle("/FieldEventSpawnReward", spawnCatch(3)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/FieldEventSpawnReward", spawnCatch(4)); err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || progressCalls != 1 || s.state.DailyNormal != 1 {
		t.Fatal("repeat catch duplicated settlement")
	}
	next, err := Open(store, s.design, s.registry, e)
	if err != nil {
		t.Fatal(err)
	}
	next.now = s.now
	next.BeginSession("restart")
	_, b, _, err = next.Handle("/FieldEventSpawnInfo", wire.AppendVarint(req(5), 2, 3))
	if err != nil {
		t.Fatal(err)
	}
	p, exists, _ := wire.Bytes(b, 1)
	if !exists || val(p, 3) != 1 || val(b, 2) != 1 {
		t.Fatal("reconnect lost durable catch progress")
	}
	caught := 0
	if err := wire.Walk(p, func(f wire.Field) error {
		if f.Number == 5 {
			caught++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if caught != 1 {
		t.Fatal("resume lost caught monster")
	}
	if _, _, _, err = next.Handle("/FieldEventSpawnReward", spawnCatch(6)); err != nil || e.calls != 1 {
		t.Fatal("restart catch retry duplicated reward")
	}
}

func TestTrackerSlotsExpiryAndDailyReset(t *testing.T) {
	s, _, _ := setup(t)
	base := s.now()
	now := base
	s.now = func() time.Time { return now }
	s.design.Tables["FieldSpawnEventTable"] = append(s.design.Tables["FieldSpawnEventTable"], gamedata.EventActionRow{Values: map[int]uint64{4: 1, 5: 2, 2: 1}, Text: map[int]string{7: "13:00:00", 1: "13:10:00"}})
	r := events.NewRegistry()
	if err := r.Replace([]events.Schedule{{UID: 3, Type: 21, ID: 1, Start: base.Add(-time.Hour).UnixMilli(), End: base.Add(48 * time.Hour).UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	s.registry = r
	if _, _, _, err := s.Handle("/FieldEventSpawnStart", spawnStart(1, 1)); err != nil {
		t.Fatal(err)
	}
	start := s.state.Spawns[3].Start
	now = base.Add(40 * time.Second)
	if _, _, _, err := s.Handle("/FieldEventSpawnReward", spawnCatch(2)); err == nil {
		t.Fatal("accepted catch at time limit")
	}
	if _, _, _, err := s.Handle("/FieldEventSpawnStart", spawnStart(3, 1)); err != nil || s.state.Spawns[3].Start != start {
		t.Fatal("expired slot was renewed")
	}
	if _, _, _, err := s.Handle("/FieldEventSpawnStart", spawnStart(4, 2)); err == nil {
		t.Fatal("future slot started early")
	}
	now = base.Add(time.Hour)
	if _, _, _, err := s.Handle("/FieldEventSpawnStart", spawnStart(5, 2)); err != nil || s.state.Spawns[3].ID != 2 {
		t.Fatal("next slot did not replace expired progress")
	}
	s.state.DailyNormal = 10
	s.state.DailySpecial = 2
	now = base.Add(24 * time.Hour)
	_, b, _, err := s.Handle("/FieldEventSpawnInfo", wire.AppendVarint(req(6), 2, 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := wire.Bytes(b, 1); ok || val(b, 2) != 0 || val(b, 3) != 0 {
		t.Fatal("daily reset kept yesterday's progress or quotas")
	}
}

func TestTrackerQuotaStopsRewardsWithoutRejectingCapture(t *testing.T) {
	s, e, _ := setup(t)
	if _, _, _, err := s.Handle("/FieldEventSpawnStart", spawnStart(1, 1)); err != nil {
		t.Fatal(err)
	}
	s.state.DailyNormal = 10
	if _, _, _, err := s.Handle("/FieldEventSpawnReward", spawnCatch(2)); err != nil {
		t.Fatal("daily reward quota rejected gameplay capture", err)
	}
	if e.calls != 0 || s.state.DailyNormal != 10 || !s.state.Spawns[3].Caught[key(1, 1)] {
		t.Fatal("quota either awarded again or failed to finish capture")
	}
}
