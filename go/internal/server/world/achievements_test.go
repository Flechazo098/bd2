package world

import (
	"bytes"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func achievementRequest(seq, group, add uint64) []byte {
	req := wire.AppendVarint(nil, 1, seq)
	req = wire.AppendVarint(req, 2, group)
	return wire.AppendVarint(req, 3, add)
}
func achievementTestService(t *testing.T, store stateio.Store) *AchievementService {
	t.Helper()
	s, err := NewAchievementService(&gamedata.AchievementCounterDesign{Groups: map[int][]int{7: {0, 1}, 9: {0}}}, store)
	if err != nil {
		t.Fatal(err)
	}
	s.BeginSession("login-a")
	return s
}
func achievementValue(t *testing.T, s *AchievementService) int64 {
	t.Helper()
	state, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	return state.Counts["7"]
}
func TestAchievementUpdatePersistsAndRetries(t *testing.T) {
	store := stateio.NewMemory()
	s := achievementTestService(t, store)
	req := achievementRequest(10, 7, 1)
	for i := 0; i < 2; i++ {
		code, body, ok, err := s.Handle("/AchievementUpdate", req)
		if err != nil || code != 167 || !ok || len(body) != 0 {
			t.Fatalf("update: %d %x %v %v", code, body, ok, err)
		}
	}
	if got := achievementValue(t, s); got != 1 {
		t.Fatalf("retry incremented count: %d", got)
	}
	reopened := achievementTestService(t, store)
	if got := achievementValue(t, reopened); got != 1 {
		t.Fatalf("reopen: %d", got)
	}
	if _, _, _, err := reopened.Handle("/AchievementUpdate", achievementRequest(10, 7, 2)); err == nil {
		t.Fatal("conflicting sequence accepted")
	}
	reopened.BeginSession("login-b")
	if _, _, _, err := reopened.Handle("/AchievementUpdate", req); err != nil {
		t.Fatal(err)
	}
	code, body, ok, err := reopened.Handle("/AchievementInfo", wire.AppendVarint(nil, 1, 11))
	var expected []byte
	for _, content := range []uint64{0, 1} {
		row := wire.AppendVarint(nil, 1, 7)
		row = wire.AppendVarint(row, 2, 2)
		if content != 0 {
			row = wire.AppendVarint(row, 3, 1000)
			row = wire.AppendVarint(row, 4, content)
		}
		expected = wire.AppendBytes(expected, 1, row)
	}
	if err != nil || code != 166 || !ok || !bytes.Equal(body, expected) {
		t.Fatalf("info mismatch: %d %x %v %v", code, body, ok, err)
	}
}
func TestAchievementInvalidRequestsDoNotMutate(t *testing.T) {
	s := achievementTestService(t, stateio.NewMemory())
	for _, req := range [][]byte{achievementRequest(0, 7, 1), achievementRequest(1, 8, 1), achievementRequest(1, 7, 0), achievementRequest(1, 7, 1<<32), {0xff}} {
		if _, _, _, err := s.Handle("/AchievementUpdate", req); err == nil {
			t.Fatalf("invalid request accepted: %x", req)
		}
	}
	if achievementValue(t, s) != 0 {
		t.Fatal("invalid request mutated counter")
	}
	s.BeginSession("")
	if _, _, _, err := s.Handle("/AchievementUpdate", achievementRequest(1, 7, 1)); err == nil {
		t.Fatal("missing session accepted")
	}
}

func TestAchievementBatchRetryKeepsEarlierReceipt(t *testing.T) {
	s := achievementTestService(t, stateio.NewMemory())
	requests := [][]byte{achievementRequest(10, 7, 1), achievementRequest(11, 7, 2)}
	for retry := 0; retry < 2; retry++ {
		for _, request := range requests {
			if _, _, _, err := s.Handle("/AchievementUpdate", request); err != nil {
				t.Fatal(err)
			}
		}
	}
	if achievementValue(t, s) != 3 {
		t.Fatal("committed batch retry duplicated increments")
	}
	if _, _, _, err := s.Handle("/AchievementUpdate", achievementRequest(300, 7, 1)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/AchievementUpdate", requests[0]); err == nil {
		t.Fatal("expired replay accepted as a new increment")
	}
	if achievementValue(t, s) != 4 {
		t.Fatal("expired replay changed progress")
	}
}

type achievementClaimsFixture struct{}

func (achievementClaimsFixture) ClaimedAchievementIDs() map[gamedata.AchievementKey]bool {
	return map[gamedata.AchievementKey]bool{{ContentsGroup: 1, GroupID: 7, ID: 1003}: true}
}
func TestAchievementInfoIncludesRealClaimState(t *testing.T) {
	s := achievementTestService(t, stateio.NewMemory())
	s.claims = achievementClaimsFixture{}
	_, body, _, err := s.Handle("/AchievementInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]byte
	if err = wire.Walk(body, func(field wire.Field) error {
		if field.Number == 1 {
			rows = append(rows, field.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("missing claim group rows: %x", body)
	}
	clear, found, err := wire.Varint(rows[1], 3)
	if err != nil || !found || clear != 1003 {
		t.Fatal("max_clear_id did not come from actual claims")
	}
}
func TestAchievementTransactionRollbackAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := achievementTestService(t, repo)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/AchievementUpdate", achievementRequest(1, 7, 1)); err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	s = achievementTestService(t, repo)
	if achievementValue(t, s) != 1 {
		t.Fatal("SQLite reopen lost count")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/AchievementUpdate", achievementRequest(2, 7, 3)); err != nil {
		t.Fatal(err)
	}
	// Repository fences a dirty rollback because other domains cache memory.
	// Reopening is the recovery boundary; this service keeps no cached counts.
	_ = op.Rollback()
	_ = repo.Close()
	recovered, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	s = achievementTestService(t, recovered)
	if achievementValue(t, s) != 1 {
		t.Fatal("rolled-back increment survived")
	}
	if _, _, _, err = s.Handle("/AchievementUpdate", achievementRequest(2, 7, 3)); err != nil {
		t.Fatal(err)
	}
	if achievementValue(t, s) != 4 {
		t.Fatal("rolled-back replay receipt survived")
	}
}
