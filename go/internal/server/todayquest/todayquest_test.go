package todayquest

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type economyFake struct {
	seen   map[string]bool
	grants int
}

func (e *economyFake) Apply(id string, c, r []gamedata.Reward) ([]byte, error) {
	if !e.seen[id] {
		e.seen[id] = true
		if len(r) > 0 {
			e.grants++
		}
	}
	return nil, nil
}

type itemsFake struct{}

func (itemsFake) GrantOnce(string, []gamedata.BattleReward) ([]player.Item, error) { return nil, nil }
func (itemsFake) GrantedItems(string) []player.Item                                { return nil }
func packet(id int, vs ...uint64) []byte {
	b := wire.AppendVarint(nil, 1, 1)
	b = wire.AppendVarint(b, 2, uint64(id))
	b = wire.AppendVarint(b, 3, 7)
	var p []byte
	for _, v := range vs {
		p = binary.AppendUvarint(p, v)
	}
	if len(p) > 0 {
		b = wire.AppendBytes(b, 4, p)
	}
	return b
}
func fixture(t *testing.T) (*Service, *economyFake, stateio.Store) {
	t.Helper()
	d := &gamedata.TodayQuestCatalog{Quests: map[int]gamedata.TodayQuest{101: {ID: 101, PackID: 7, NextID: 102, ConditionType: 19, ConditionCount: 1}, 102: {ID: 102, PackID: 7, PriorID: 101, ConditionType: 2, ConditionCount: 2, MagicValues: []uint64{71, 72}, Rewards: []gamedata.Reward{{Type: 4, Count: 10}}, ReputationCompleteID: 1}}, Limit: 1, PostCount: 1, Reset: gamedata.FieldResetSchedule{WeeklyDay: time.Monday, DailyReset: 9 * time.Hour}}
	e := &economyFake{seen: map[string]bool{}}
	store := stateio.NewMemory()
	s, err := Open(store, d, e, itemsFake{}, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
	s.CompleteReputation = func(string, int, uint64) ([]byte, error) { return wire.AppendVarint(nil, 2, 2), nil }
	s.design.AchievementScore = 7
	s.CompleteAchievement = func(string) error { return nil }
	return s, e, store
}
func call(t *testing.T, s *Service, path string, id int, vs ...uint64) []byte {
	t.Helper()
	_, b, ok, e := s.Handle(path, packet(id, vs...))
	if e != nil || !ok {
		t.Fatalf("%s %d: %v", path, id, e)
	}
	return b
}
func TestChainCompletionRestartReplayAndWeeklyReset(t *testing.T) {
	s, e, store := fixture(t)
	call(t, s, "/TodayQuestInfo", 0)
	if _, _, _, err := s.Handle("/QuestAccept", packet(102)); err == nil {
		t.Fatal("accepted arbitrary continuation")
	}
	call(t, s, "/QuestAccept", 101)
	if _, _, _, err := s.Handle("/QuestClear", packet(101)); err == nil {
		t.Fatal("cleared without progress")
	}
	call(t, s, "/QuestUpdate", 101, 1)
	call(t, s, "/QuestClear", 101)
	if _, _, _, err := s.Handle("/QuestUpdate", packet(102, 999)); err == nil {
		t.Fatal("foreign object accepted")
	}
	call(t, s, "/QuestUpdate", 102, 71)
	call(t, s, "/QuestUpdate", 102, 71)
	if _, _, _, err := s.Handle("/QuestClear", packet(102)); err == nil {
		t.Fatal("duplicate object counted twice")
	}
	call(t, s, "/QuestUpdate", 102, 72)
	b := call(t, s, "/QuestClear", 102)
	if e.grants != 1 {
		t.Fatal(e.grants)
	}
	reopened, err := Open(store, s.design, e, itemsFake{}, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.now
	reopened.CompleteReputation = s.CompleteReputation
	reopened.CompleteAchievement = s.CompleteAchievement
	if got := call(t, reopened, "/QuestClear", 102); !bytes.Equal(got, b) || e.grants != 1 {
		t.Fatal("restart reward replay changed")
	}
	if n, err := reopened.Score(); err != nil || n != 7 {
		t.Fatalf("score %d %v", n, err)
	}
	if _, _, _, err := s.Handle("/QuestAccept", packet(101)); err == nil {
		t.Fatal("completed root accepted")
	}
	reopened.now = func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) }
	call(t, reopened, "/TodayQuestInfo", 0)
	call(t, reopened, "/QuestAccept", 101)
	if n, err := reopened.Score(); err != nil || n != 7 {
		t.Fatalf("weekly reset lost score %d %v", n, err)
	}
}
func TestGiveUpRestoresQuotaAndClearsPriorChain(t *testing.T) {
	s, _, _ := fixture(t)
	call(t, s, "/QuestAccept", 101)
	call(t, s, "/QuestUpdate", 101, 1)
	call(t, s, "/QuestClear", 101)
	call(t, s, "/QuestGiveUp", 102)
	call(t, s, "/QuestAccept", 101)
	st, err := s.load()
	if err != nil || st.Cleared[101] {
		t.Fatalf("prior clear retained: %v", err)
	}
}

func TestPackInfoResetsExpiredSQLiteCommissionAtWeeklyBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	s, economy, _ := fixture(t)
	s.store = stateio.EntrySnapshotStore{Entries: repo, Domain: "missions", Bucket: "gameplay"}
	now := time.Date(2026, 10, 11, 23, 59, 59, 0, time.UTC)
	s.now = func() time.Time { return now }
	call(t, s, "/QuestAccept", 101)
	call(t, s, "/QuestUpdate", 101, 1)
	call(t, s, "/QuestClear", 101)
	call(t, s, "/QuestUpdate", 102, 71, 72)
	call(t, s, "/QuestClear", 102)
	st, err := s.load()
	if err != nil || s.secondsLeft(st) != 1 {
		t.Fatalf("weekly countdown: %d %v", s.secondsLeft(st), err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	s.store = stateio.EntrySnapshotStore{Entries: reopened, Domain: "missions", Bucket: "gameplay"}
	now = now.Add(time.Second)
	// PackInGameInfo calls Info without an intervening TodayQuestInfo request.
	rows, cleared, err := s.Info(7)
	if err != nil || len(rows) != 0 || len(cleared) != 0 {
		t.Fatalf("expired pack tasks: %v %v %v", rows, cleared, err)
	}
	st, err = s.load()
	if err != nil || s.secondsLeft(st) != 7*24*60*60 {
		t.Fatalf("new weekly countdown: %d %v", s.secondsLeft(st), err)
	}
	if score, err := s.Score(); err != nil || score != 7 {
		t.Fatalf("prior score: %d %v", score, err)
	}
	call(t, s, "/QuestAccept", 101)
	call(t, s, "/QuestUpdate", 101, 1)
	call(t, s, "/QuestClear", 101)
	call(t, s, "/QuestUpdate", 102, 71, 72)
	call(t, s, "/QuestClear", 102)
	if score, err := s.Score(); err != nil || score != 14 || economy.grants != 2 {
		t.Fatalf("new week score/reward: %d %d %v", score, economy.grants, err)
	}
}

func TestCurrentCommissionConditionsRestoreProgressAndRejectForeignObjects(t *testing.T) {
	// Proto.Net.Define_QuestConditionType and PackManager.RefreshQuestCondition:
	// Hunt/ TalkManual use Value; Collection/ObjectMove/FieldResearchObject use ObjectId.
	for _, condition := range []int{1, 2, 9, 18, 19} {
		t.Run(fmt.Sprint(condition), func(t *testing.T) {
			s, _, _ := fixture(t)
			q := s.design.Quests[101]
			q.ConditionType, q.ConditionCount = condition, 2
			q.MagicValues = []uint64{71, 72}
			s.design.Quests[101] = q
			call(t, s, "/QuestAccept", 101)
			objectCondition := condition == 2 || condition == 9 || condition == 18
			if objectCondition {
				call(t, s, "/QuestUpdate", 101, 71)
				call(t, s, "/QuestUpdate", 101, 71)
				if _, _, _, err := s.Handle("/QuestUpdate", packet(101, 999)); err == nil {
					t.Fatal("foreign object accepted")
				}
			} else {
				call(t, s, "/QuestUpdate", 101, 1)
				call(t, s, "/QuestUpdate", 101, 0)
				if _, _, _, err := s.Handle("/QuestUpdate", packet(101, 3)); err == nil {
					t.Fatal("progress exceeded condition count")
				}
			}
			rows, _, err := s.Info(7)
			if err != nil || len(rows) != 1 {
				t.Fatalf("restored rows: %v %v", rows, err)
			}
			if objectCondition {
				object, _, _ := wire.Varint(rows[0], 3)
				if object != 71 {
					t.Fatalf("object progress %d", object)
				}
			} else {
				value, _, _ := wire.Varint(rows[0], 2)
				if value != 1 {
					t.Fatalf("scalar progress %d", value)
				}
			}
			if _, _, _, err := s.Handle("/QuestClear", packet(101)); err == nil {
				t.Fatal("partial progress cleared")
			}
			if objectCondition {
				call(t, s, "/QuestUpdate", 101, 72)
			} else {
				call(t, s, "/QuestUpdate", 101, 2)
			}
			call(t, s, "/QuestClear", 101)
		})
	}
}
