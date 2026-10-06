package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type overwhelmFailedState struct{ stateio.Store }

func (s overwhelmFailedState) Save(name string, b []byte) error {
	if name == "field_monster_runtime" {
		return fmt.Errorf("failed receipt")
	}
	return s.Store.Save(name, b)
}

type overwhelmPersistedEconomy struct{ stateio.Store }

func (s overwhelmPersistedEconomy) Apply(_ string, _ []gamedata.Reward, rewards []gamedata.Reward) ([]byte, error) {
	if e := s.Save("overwhelm_test_reward", []byte{1}); e != nil {
		return nil, e
	}
	return nil, nil
}
func TestOverwhelmRewardAndReceiptRollbackTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, e := accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s := testService()
	if err := s.AttachFieldMonsterState(overwhelmFailedState{repo}); err != nil {
		t.Fatal(err)
	}
	s.BeginSession("login")
	s.monsterLoader = func(int) ([]gamedata.FieldMonsterDesign, error) {
		return []gamedata.FieldMonsterDesign{{ID: 9, GroupID: 7, BattleDeck: 33, UseBattleSkip: 1, RegenSeconds: 10, Reward: gamedata.Reward{Type: 5, ID: 400, Count: 2}}}, nil
	}
	s.researchEconomy = overwhelmPersistedEconomy{repo}
	s.AttachOverwhelmAuthorization(func(string, uint64) error { return repo.Save("overwhelm_test_authorization", []byte{1}) })
	op, e := repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = s.Handle("/Overwhelm", overwhelmRequest(1, 9)); e == nil {
		t.Fatal("expected failed final receipt")
	}
	if e = op.Rollback(); e != nil && !errors.Is(e, stateio.ErrStateRecoveryRequired) {
		t.Fatal(e)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, e = accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, name := range []string{"overwhelm_test_authorization", "overwhelm_test_reward", "field_monster_runtime"} {
		b, e := repo.Load(name)
		if e != nil || b != nil {
			t.Fatal("partial batch survived rollback", name, e)
		}
	}
}

func overwhelmRequest(seq uint64, ids ...uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	for _, id := range ids {
		m := wire.AppendVarint(nil, 1, 7)
		m = wire.AppendVarint(m, 2, id)
		m = wire.AppendVarint(m, 3, 33)
		m = wire.AppendVarint(m, 4, 2)
		b = wire.AppendBytes(b, 2, m)
	}
	return b
}
func TestOverwhelmBatchPrevalidationAuthorizationAndGenerationReplay(t *testing.T) {
	s := testService()
	store := stateio.NewMemory()
	if err := s.AttachFieldMonsterState(store); err != nil {
		t.Fatal(err)
	}
	s.BeginSession("login")
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s.monsterNow = func() time.Time { return now }
	s.monsterLoader = func(int) ([]gamedata.FieldMonsterDesign, error) {
		return []gamedata.FieldMonsterDesign{{ID: 9, GroupID: 7, BattleDeck: 33, UseBattleSkip: 1, RegenSeconds: 10, Reward: gamedata.Reward{Type: 5, ID: 400, Count: 2}}, {ID: 10, GroupID: 7, BattleDeck: 33, UseBattleSkip: 1, RegenSeconds: 10, Reward: gamedata.Reward{Type: 5, ID: 400, Count: 2}}}, nil
	}
	eco := &researchEconomySpy{}
	s.researchEconomy = eco
	auth := 0
	s.AttachOverwhelmAuthorization(func(identity string, n uint64) error {
		if n != 2 {
			return fmt.Errorf("wrong batch size")
		}
		auth++
		return nil
	})
	if _, _, _, e := s.Handle("/Overwhelm", overwhelmRequest(1, 9, 999)); e == nil || eco.calls != 0 || auth != 0 {
		t.Fatal("invalid tail partially settled batch", e)
	}
	req := overwhelmRequest(2, 9, 10)
	code, first, _, e := s.Handle("/Overwhelm", req)
	if e != nil || code != 275 || eco.calls != 2 || auth != 1 {
		t.Fatal("valid batch not settled", code, eco.calls, auth, e)
	}
	now = now.Add(11 * time.Second)
	_, retry, _, e := s.Handle("/Overwhelm", req)
	if e != nil || !bytes.Equal(first, retry) || eco.calls != 2 || auth != 1 {
		t.Fatal("retry awarded or authorized twice", e)
	}
	if _, _, _, e = s.Handle("/Overwhelm", overwhelmRequest(2, 10, 9)); e == nil {
		t.Fatal("sequence reuse accepted")
	}
	if _, _, _, e = s.Handle("/Overwhelm", overwhelmRequest(3, 9, 10)); e != nil || eco.calls != 4 {
		t.Fatal("new generation did not award", eco.calls, e)
	}
}
