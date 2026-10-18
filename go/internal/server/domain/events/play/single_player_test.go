package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func testRow(fields ...uint64) []byte {
	var out []byte
	for i := 0; i < len(fields); i += 2 {
		out = wire.AppendVarint(out, int(fields[i]), fields[i+1])
	}
	return out
}

// The small synthetic designs express workflow rules; they are independent of
// the installed season's resource identities and table contents.
type playHarness struct {
	service  *Service
	ctx      command.Context
	design   *gamedata.EventPlayCatalog
	registry *events.Registry
	now      time.Time
}

type testEconomy struct{}

func (testEconomy) Apply(ctx command.Context, identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	if _, found, err := ctx.State.LoadEntry("test-wallet", "grants", identity); err != nil {
		return nil, err
	} else if found {
		return nil, fmt.Errorf("duplicate economy identity")
	}
	balances := map[uint64]uint64{}
	raw, _ := ctx.State.Load("test-wallet")
	if raw != nil {
		if err := json.Unmarshal(raw, &balances); err != nil {
			return nil, err
		}
	}
	for _, cost := range costs {
		if balances[cost.Type] < cost.Count {
			return nil, fmt.Errorf("insufficient balance")
		}
		balances[cost.Type] -= cost.Count
	}
	for _, reward := range rewards {
		balances[reward.Type] += reward.Count
	}
	raw, _ = json.Marshal(balances)
	if err := ctx.State.Save("test-wallet", raw); err != nil {
		return nil, err
	}
	if err := ctx.State.PutEntry("test-wallet", "grants", identity, []byte{1}); err != nil {
		return nil, err
	}
	return testRow(1, uint64(len(rewards))), nil
}

type emptyRankStore struct{}
type emptyRankTx struct{}

func (emptyRankTx) Get(string, string) ([]byte, bool, error)                  { return nil, false, nil }
func (emptyRankTx) Put(string, string, []byte) error                          { return nil }
func (emptyRankTx) Scan(string, string, string, int) ([]ranking.Entry, error) { return nil, nil }
func (emptyRankStore) View(_ context.Context, fn func(ranking.Tx) error) error {
	return fn(emptyRankTx{})
}
func (emptyRankStore) Update(_ context.Context, fn func(ranking.Tx) error) error {
	return fn(emptyRankTx{})
}

func newPlayHarness(t *testing.T) *playHarness {
	t.Helper()
	h := &playHarness{ctx: command.Context{Identity: command.Identity{AccountID: "player", SessionID: "first-login"}, State: stateio.NewMemory()}, design: &gamedata.EventPlayCatalog{Tables: map[string][][]byte{}}, registry: events.NewRegistry(), now: time.UnixMilli(1000000000)}
	h.design.Tables["PackEventMiniGameTable"] = [][]byte{testRow(8, 10, 3, 5, 4, 1), testRow(8, 20, 3, 6), testRow(8, 30, 3, 12), testRow(8, 40, 3, 13), testRow(8, 50, 3, 9)}
	var schedules []events.Schedule
	for _, id := range []uint64{10, 20, 30, 40, 50} {
		schedules = append(schedules, events.Schedule{UID: id + 100, Type: 11, ID: id, Start: h.now.UnixMilli() - 1000, End: h.now.UnixMilli() + 86400000})
	}
	if err := h.registry.Replace(schedules); err != nil {
		t.Fatal(err)
	}
	h.design.Tables["FieldMiniGameSurvivalTable"] = [][]byte{testRow(9, 1, 7, 1, 15, 1, 21, 2, 24, 30, 10, 500)}
	h.design.Tables["FieldMiniGameCharTable"] = [][]byte{wire.AppendDouble(wire.AppendDouble(testRow(11, 1, 13, 1, 20, 100, 19, 2, 3, 1, 2, 1), 8, 1), 10, 1)}
	h.design.Tables["FieldMiniGameMapGroupTable"] = [][]byte{testRow(1, 1, 2, 1)}
	h.design.Tables["FieldMiniGameMapTable"] = [][]byte{testRow(2, 1, 3, 1, 4, 1, 9, 1, 6, 50, 8, 43)}
	h.design.Tables["FieldMiniGameMonsterTable"] = [][]byte{testRow(2, 1, 5, 1, 12, 2, 14, 10, 11, 1, 6, 1)}
	h.design.Tables["FieldMiniGameSurvivalBoxTable"] = [][]byte{testRow(2, 1, 3, 11, 3, 12)}
	h.design.Tables["FieldMiniGameSurvivalItemTable"] = [][]byte{testRow(1, 11, 3, 3, 4, 10, 6, 100), testRow(1, 12, 3, 4, 4, 5, 6, 100)}
	h.design.Tables["FieldMiniGameSkillGroupTable"] = [][]byte{testRow(1, 1, 2, 3, 5, 1, 7, 1), testRow(1, 2, 2, 2, 5, 2), testRow(1, 3, 2, 2, 5, 1, 7, 2)}
	h.design.Tables["FieldMiniGameSkillTable"] = [][]byte{testRow(7, 1, 8, 1), testRow(7, 1, 8, 2), testRow(7, 1, 8, 3), testRow(7, 2, 8, 1), testRow(7, 2, 8, 2)}
	h.design.Tables["FieldMiniGameCharLevelTable"] = [][]byte{testRow(1, 1, 2, 1, 3, 10), testRow(1, 1, 2, 2, 3, 20), testRow(1, 1, 2, 3, 3, 50)}
	h.design.Tables["FieldMiniGameCollectionTable"] = [][]byte{testRow(1, 1, 3, 5), testRow(1, 3, 3, 6, 7, 2)}
	h.design.Tables["FieldMiniGameUpgradeGroupTable"] = [][]byte{testRow(1, 9, 2, 1)}
	h.design.Tables["FieldMiniGameUpgradeTable"] = [][]byte{testRow(1, 9, 2, 1), wire.AppendDouble(testRow(1, 9, 2, 2, 3, 10), 4, 20)}
	h.reopen(t)
	return h
}

func (h *playHarness) reopen(t *testing.T) {
	t.Helper()
	s, err := Open(h.ctx, stateio.RootStore{}, h.design, func(uint64) (*gamedata.EventField, error) { return nil, fmt.Errorf("fixture has no field game") }, h.registry, testEconomy{})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return h.now }
	s.actionMissionCharacters = map[uint64]uint64{6: 1, 7: 2, 8: 3}
	ranks, err := ranking.NewArcade(context.Background(), emptyRankStore{})
	if err != nil {
		t.Fatal(err)
	}
	s.AttachArcadeRankings(ranks, func(command.Context) (ranking.ArcadeProfile, error) {
		return ranking.ArcadeProfile{Owner: 7, Name: "player"}, nil
	})
	h.service = s
}
func (h *playHarness) call(t *testing.T, path string, req []byte) []byte {
	t.Helper()
	_, out, handled, err := h.service.Handle(h.ctx, path, req)
	if err != nil || !handled {
		t.Fatalf("%s failed: %v", path, err)
	}
	return out
}
func (h *playHarness) reject(t *testing.T, path string, req []byte) {
	t.Helper()
	before, _ := h.ctx.State.Load("eventplay")
	_, _, _, err := h.service.Handle(h.ctx, path, req)
	if err == nil {
		t.Fatalf("%s accepted invalid transition", path)
	}
	after, _ := h.ctx.State.Load("eventplay")
	if !bytes.Equal(before, after) {
		t.Fatalf("%s persisted failed transition", path)
	}
}
func (h *playHarness) balance(t *testing.T, typ uint64) uint64 {
	t.Helper()
	raw, err := h.ctx.State.Load("test-wallet")
	if err != nil {
		t.Fatal(err)
	}
	var values map[uint64]uint64
	if raw != nil {
		if err := json.Unmarshal(raw, &values); err != nil {
			t.Fatal(err)
		}
	}
	return values[typ]
}

func TestSurvivalReconnectionProgressRewardAndUpgradeReset(t *testing.T) {
	h := newPlayHarness(t)
	start := testRow(1, 1, 2, 110, 3, 1, 4, 1, 5, 1)
	first := h.call(t, "/MiniGameSurvivalStart", start)
	h.reopen(t)
	if !bytes.Equal(first, h.call(t, "/MiniGameSurvivalStart", start)) {
		t.Fatal("start retry changed persisted seed")
	}
	h.ctx.SessionID = "reconnected"
	h.now = h.now.Add(time.Second)
	play := testRow(1, 2, 2, 100, 3, 10, 4, 5, 7, 1)
	play = wire.AppendBytes(play, 6, testRow(1, 1, 2, 1))
	play = wire.AppendBytes(play, 5, testRow(1, 11, 2, 1))
	play = wire.AppendBytes(play, 5, testRow(1, 12, 2, 1))
	h.call(t, "/MiniGameSurvivalPlay", play)
	h.reject(t, "/MiniGameSurvivalPlay", wire.AppendBytes(testRow(1, 3, 2, 100, 4, 5), 5, testRow(1, 12, 2, 1)))
	offers := h.call(t, "/MiniGameSurvivalSkillSelectList", testRow(1, 4))
	ids, _ := list(offers, 2)
	for _, id := range ids {
		if id == 3 {
			t.Fatal("other character's unique weapon was offered")
		}
	}
	h.call(t, "/MiniGameSurvivalSkillUp", testRow(1, 5, 2, ids[0]))
	end := testRow(1, 6, 3, 1, 4, 1, 5, 5, 6, 10)
	result := h.call(t, "/MiniGameSurvivalEnd", end)
	if h.balance(t, 43) != 55 {
		t.Fatal("first clear and collected coins were not credited")
	}
	h.reopen(t)
	if !bytes.Equal(result, h.call(t, "/MiniGameSurvivalEnd", end)) || h.balance(t, 43) != 55 {
		t.Fatal("persisted End retry duplicated rewards")
	}
	h.reject(t, "/MiniGameSurvivalEnd", testRow(1, 7, 3, 1))
	h.call(t, "/MiniGameSurvivalCharUpgrade", testRow(1, 8, 2, 9, 3, 2))
	if h.balance(t, 43) != 45 {
		t.Fatal("upgrade did not consume currency")
	}
	h.call(t, "/MiniGameSurvivalStart", testRow(1, 9, 2, 110, 3, 1, 4, 1, 5, 1))
	if h.service.state.Runs["Survival"].HP != 120 {
		t.Fatal("permanent HP upgrade was not applied to new run")
	}
	h.call(t, "/MiniGameSurvivalEnd", testRow(1, 10, 2, 1))
	h.call(t, "/MiniGameSurvivalCharUpgradeReset", testRow(1, 11))
	if h.balance(t, 43) != 55 {
		t.Fatal("reset did not refund actual upgrade cost")
	}
	h.reopen(t)
	info := h.call(t, "/MiniGameSurvivalInfo", testRow(1, 12))

	var collections int
	_ = wire.Walk(info, func(f wire.Field) error {
		if f.Number == 9 {
			collections++
		}
		return nil
	})
	if collections != 2 {
		t.Fatal("collected weapon/item entries were not persisted")
	}
}

func TestSurvivalWeightedDropsCannotBeCollectedTwiceOrRerolledByRetry(t *testing.T) {
	h := newPlayHarness(t)
	h.design.Tables["FieldMiniGameSurvivalBoxTable"] = [][]byte{testRow(1, 1, 2, 1, 3, 11, 3, 12, 4, 3, 4, 1)}
	h.call(t, "/MiniGameSurvivalStart", testRow(1, 1, 2, 110, 3, 1, 4, 1, 5, 1))
	h.now = h.now.Add(time.Second)
	// A separately evaluated current-client Well512 vector for seed 1,000,000,000
	// draws 1 modulo 4, selecting the first weighted item and excluding the coin.
	bad := wire.AppendBytes(wire.AppendBytes(testRow(1, 2, 2, 100, 4, 5, 7, 1), 6, testRow(1, 1, 2, 1)), 5, testRow(1, 12, 2, 1))
	h.reject(t, "/MiniGameSurvivalPlay", bad)
	good := wire.AppendBytes(wire.AppendBytes(testRow(1, 2, 2, 100, 3, 10, 7, 1), 6, testRow(1, 1, 2, 1)), 5, testRow(1, 11, 2, 1))
	out := h.call(t, "/MiniGameSurvivalPlay", good)
	h.reopen(t)
	if !bytes.Equal(out, h.call(t, "/MiniGameSurvivalPlay", good)) {
		t.Fatal("drop retry advanced random state")
	}
	h.reject(t, "/MiniGameSurvivalPlay", wire.AppendBytes(testRow(1, 3, 2, 100, 3, 10), 5, testRow(1, 11, 2, 1)))
	h.call(t, "/MiniGameSurvivalEnd", testRow(1, 4, 2, 1, 3, 1, 4, 1, 6, 10))
	if h.balance(t, 43) != 0 {
		t.Fatal("an unselected drop granted currency")
	}
}

func TestSurvivalInsufficientUpgradePreservesStateAndResources(t *testing.T) {
	h := newPlayHarness(t)
	h.call(t, "/MiniGameSurvivalInfo", testRow(1, 1))
	h.reject(t, "/MiniGameSurvivalCharUpgrade", testRow(1, 2, 2, 9, 3, 2))
	h.reopen(t)
	if h.balance(t, 43) != 0 || len(h.service.state.Upgrades) != 0 {
		t.Fatal("failed upgrade altered persisted resources")
	}
}

func TestSichuanStarsAndChallengeClearRewardsSurviveLogin(t *testing.T) {
	h := newPlayHarness(t)
	h.design.Tables["SichuanEventTable"] = [][]byte{testRow(1, 1, 2, 1, 3, 9), testRow(1, 1, 2, 2, 3, 9), testRow(1, 2, 2, 1, 3, 9)}
	h.design.Tables["SichuanStageTable"] = [][]byte{testRow(2, 9, 8, 6, 3, 300, 3, 200, 3, 100)}
	h.design.Tables["SichuanRewardTable"] = [][]byte{testRow(1, 1, 2, 1, 3, 3, 4, 2, 5, 1, 6, 27), testRow(1, 2, 2, 1, 3, 1, 4, 1, 5, 2, 6, 27)}
	start := func(seq, group, stage uint64) []byte {
		return wire.AppendBytes(testRow(1, seq, 2, 120, 3, group, 4, stage), 5, testRow(1, 1001, 2, 4))
	}
	end := func(seq, score uint64) []byte {
		out := testRow(1, seq, 3, score*13+7, 4, score%11)
		for i := uint64(1); i <= 2; i++ {
			out = wire.AppendBytes(out, 5, testRow(1, i, 2, 1001, 2, 1001))
		}
		return out
	}
	h.reject(t, "/MiniGameSichuanStart", start(1, 1, 2))
	h.call(t, "/MiniGameSichuanStart", start(2, 1, 1))
	h.reject(t, "/MiniGameSichuanEnd", testRow(1, 3, 3, 1307, 4, 2))
	h.call(t, "/MiniGameSichuanEnd", end(4, 90))
	if h.balance(t, 27) != 2 {
		t.Fatal("normal accumulated stars did not grant reward")
	}
	h.reopen(t)
	h.ctx.SessionID = "second"
	h.call(t, "/MiniGameSichuanStart", start(5, 1, 2))
	h.call(t, "/MiniGameSichuanEnd", end(6, 180))
	if h.balance(t, 27) != 2 {
		t.Fatal("normal reward repeated after another level")
	}
	// LocalTextTable 125517 grants furniture for every challenge clear, regardless of rank.
	h.call(t, "/MiniGameSichuanStart", start(7, 2, 1))
	response := h.call(t, "/MiniGameSichuanEnd", end(8, 180))
	if h.balance(t, 27) != 3 {
		t.Fatal("challenge first clear reward missing")
	}
	h.reopen(t)
	if !bytes.Equal(response, h.call(t, "/MiniGameSichuanEnd", end(8, 180))) || h.balance(t, 27) != 3 {
		t.Fatal("challenge retry duplicated reward")
	}
	h.call(t, "/MiniGameSichuanStart", start(9, 2, 1))
	response = h.call(t, "/MiniGameSichuanEnd", end(10, 120))
	if h.balance(t, 27) != 4 {
		t.Fatal("second challenge clear did not grant its furniture reward")
	}
	h.reopen(t)
	if !bytes.Equal(response, h.call(t, "/MiniGameSichuanEnd", end(10, 120))) || h.balance(t, 27) != 4 {
		t.Fatal("second challenge retry duplicated reward")
	}
	h.call(t, "/MiniGameSichuanStart", start(11, 2, 1))
	h.call(t, "/MiniGameSichuanEnd", end(12, 150))
	if h.balance(t, 27) != 5 {
		t.Fatal("slower challenge clear did not grant its furniture reward")
	}
	h.call(t, "/MiniGameSichuanStart", start(13, 2, 1))
	h.call(t, "/MiniGameSichuanEnd", wire.AppendVarint(end(14, 150), 2, 1))
	if h.balance(t, 27) != 5 {
		t.Fatal("abandoned challenge granted a clear reward")
	}
	if records := h.service.RankRecords(); len(records) != 1 || records[0].Score != 120 {
		t.Fatal("challenge did not retain the smaller clear time")
	}
	info := h.call(t, "/MiniGameSichuanInfo", testRow(1, 15, 2, 120))
	schedule, _, _ := wire.Bytes(info, 1)
	var rewardClaims int
	_ = wire.Walk(schedule, func(f wire.Field) error {
		if f.Number == 7 {
			rewardClaims++
		}
		return nil
	})
	if rewardClaims != 2 {
		t.Fatal("claimed star/challenge rewards were not persisted")
	}
}

func TestHopscotchPartialPlayClearFullAndContinueRewardsAreDistinct(t *testing.T) {
	h := newPlayHarness(t)
	h.design.Tables["HopscotchDefaultTable"] = [][]byte{testRow(4, 80, 9, 300, 1, 12, 2, 11)}
	h.design.Tables["HopscotchStageTable"] = [][]byte{testRow(6, 1, 1, 2, 2, 1), testRow(6, 2, 1, 4, 2, 3)}
	for _, id := range []uint64{1, 2, 3, 4, 11, 12} {
		h.design.Tables["HopscotchGalleryTable"] = append(h.design.Tables["HopscotchGalleryTable"], testRow(4, id))
	}
	for _, v := range [][3]uint64{{1, 1, 1}, {2, 2, 1}, {3, 6, 1}, {4, 5, 0}, {5, 3, 0}, {6, 4, 0}} {
		h.design.Tables["HopscotchRewardTable"] = append(h.design.Tables["HopscotchRewardTable"], testRow(3, v[0], 2, v[1], 1, v[2], 7, 25, 6, v[0], 4, 1))
	}
	play := func(startSeq, endSeq, stage, area, elapsed, continued uint64) {
		t.Helper()
		h.call(t, "/MiniGameHopscotchGameStart", testRow(1, startSeq, 2, 130, 3, stage))
		h.call(t, "/MiniGameHopscotchGameEnd", testRow(1, endSeq, 2, area, 3, elapsed, 4, continued))
	}
	play(1, 2, 1, 40, 5000, 0)
	if h.balance(t, 25) != 1 || len(h.service.RankRecords()) != 0 {
		t.Fatal("partial play earned a clear reward or ranked")
	}
	play(3, 4, 1, 85, 4000, 1)
	if h.balance(t, 25) != 2 {
		t.Fatal("continued clear did not earn only clear reward")
	}
	play(5, 6, 1, 100, 3000, 0)
	if h.balance(t, 25) != 4 {
		t.Fatal("full completion and any-stage no-continue rewards missing")
	}
	h.reopen(t)
	h.ctx.SessionID = "second"
	play(7, 8, 1, 100, 4500, 0)
	if h.balance(t, 25) != 4 {
		t.Fatal("repeated clear duplicated rewards")
	}
	records := h.service.RankRecords()
	if len(records) != 1 || records[0].ClearTime != 3000 {
		t.Fatal("slower full clear replaced best time")
	}
	play(9, 10, 2, 85, 4000, 1)
	if h.balance(t, 25) != 6 {
		t.Fatal("all-stage play/clear rewards missing")
	}
	info := h.call(t, "/MiniGameHopscotchInfo", testRow(1, 11, 2, 130))
	galleries, _ := list(info, 1)
	if len(galleries) != 6 {
		t.Fatal("stage and all-stage galleries missing after replay")
	}
}

func TestPackManCommitsStagesAndChecksScoreAcrossLogin(t *testing.T) {
	h := newPlayHarness(t)
	h.design.Tables["PackManDefaultTable"] = [][]byte{testRow(15, 1)}
	h.design.Tables["PackManModeTable"] = [][]byte{testRow(2, 1, 5, 1, 4, 1), testRow(2, 1, 5, 2, 4, 2)}
	h.design.Tables["PackManModeRuleTable"] = [][]byte{testRow(2, 1, 4, 2), testRow(2, 2, 8, 2000, 6, 1000)}
	for i, unit := range []uint64{1, 1, 10, 20, 1, 20} {
		h.design.Tables["PackManScoreTable"] = append(h.design.Tables["PackManScoreTable"], testRow(1, uint64(i+1), 2, unit))
	}
	history := func(seconds, coins, chain, escapes uint64) []byte {
		return wire.AppendBytes(testRow(1, seconds, 2, coins, 5, chain, 6, escapes), 7, testRow(1, 1, 2, 1, 3, 1, 4, seconds*1000))
	}
	h.call(t, "/MiniGamePackManStart", testRow(1, 1, 2, 140, 3, 1))
	h.now = h.now.Add(time.Second)
	h.reject(t, "/MiniGamePackManStageClear", wire.AppendBytes(testRow(1, 2, 2, 2), 3, history(1, 1, 0, 1)))
	stage := wire.AppendBytes(testRow(1, 3, 2, 1), 3, history(1, 1, 0, 1))
	h.call(t, "/MiniGamePackManStageClear", stage)
	h.reopen(t)
	h.ctx.SessionID = "second"
	h.now = h.now.Add(time.Second)
	end := wire.AppendBytes(testRow(1, 4, 2, 3), 4, history(2, 2, 0, 1))
	h.call(t, "/MiniGamePackManEnd", end)
	h.call(t, "/MiniGamePackManStart", testRow(1, 5, 2, 140, 3, 2))
	h.now = h.now.Add(time.Second)
	h.call(t, "/MiniGamePackManStageClear", wire.AppendBytes(testRow(1, 6, 2, 1), 3, history(1, 1, 1, 1)))
	h.now = h.now.Add(2 * time.Second)
	end = wire.AppendBytes(testRow(1, 7, 2, 2, 5, 999), 4, history(3, 2, 2, 1))
	h.reject(t, "/MiniGamePackManEnd", end)
	end = wire.AppendBytes(testRow(1, 8, 2, 2, 5, 27), 4, history(3, 2, 2, 1))
	response := h.call(t, "/MiniGamePackManEnd", end)
	if num(response, 1) != 1 {
		t.Fatal("challenge result did not report new record")
	}
	h.reopen(t)
	if !bytes.Equal(response, h.call(t, "/MiniGamePackManEnd", end)) {
		t.Fatal("PackMan end retry changed new-record result")
	}
	info := h.call(t, "/MiniGamePackManInfo", testRow(1, 9, 2, 140))
	if num(info, 1) != 27 {
		t.Fatal("PackMan challenge best did not survive login")
	}
}

func TestRhythmModeRecordsAndClientChecksumPersistIndependently(t *testing.T) {
	h := newPlayHarness(t)
	h.design.Tables["RhythmGameMusicTable"] = [][]byte{testRow(5, 1, 8, 1000, 2, 1000, 1, 1000, 10, 2, 4, 2)}
	h.design.Tables["RhythmGameCharTable"] = [][]byte{testRow(2, 1)}
	end := func(seq, score, grade, combo uint64, checksum string, judgments ...[]byte) []byte {
		out := wire.AppendString(testRow(1, seq, 2, grade, 4, score, 6, combo), 5, checksum)
		for _, judgment := range judgments {
			out = wire.AppendBytes(out, 3, judgment)
		}
		return out
	}
	h.call(t, "/MiniGameRhythmPlayStart", testRow(1, 1, 2, 150, 3, 1, 4, 1, 5, 1))
	h.reject(t, "/MiniGameRhythmPlayEnd", end(2, 500, 1, 2, "invalid", testRow(1, 1, 2, 2)))
	// Four GetRandom(11111, 99999) calls on the official 2.36.7 DLL's Well512,
	// using seeds 511, 714, and 815 from owner + score + seq + weighted judgments.
	first := end(2, 500, 1, 2, "46791-93319-66855-91591", testRow(1, 1, 2, 2))
	response := h.call(t, "/MiniGameRhythmPlayEnd", first)
	h.reopen(t)
	if !bytes.Equal(response, h.call(t, "/MiniGameRhythmPlayEnd", first)) {
		t.Fatal("rhythm retry changed persisted result")
	}
	h.call(t, "/MiniGameRhythmPlayStart", testRow(1, 3, 2, 150, 3, 1, 4, 1, 5, 2))
	h.call(t, "/MiniGameRhythmPlayEnd", end(4, 700, 2, 1, "74755-89667-46635-29179", testRow(1, 1, 2, 1), testRow(1, 2, 2, 1)))
	h.call(t, "/MiniGameRhythmPlayStart", testRow(1, 5, 2, 150, 3, 1, 4, 1, 5, 0))
	h.call(t, "/MiniGameRhythmPlayEnd", end(6, 800, 1, 2, "82423-43279-74967-75511", testRow(1, 1, 2, 2)))
	h.reopen(t)
	records := h.service.RankRecords()
	if len(records) != 2 {
		t.Fatal("normal rhythm ranked or separate difficulties collapsed")
	}
	modes := map[uint64]float64{}
	for _, r := range records {
		if _, exists := modes[r.Mode]; exists {
			t.Fatal("duplicate ranked rhythm difficulty")
		}
		modes[r.Mode] = r.Score
	}
	if len(modes) != 2 || modes[1] != 500 || modes[2] != 700 {
		t.Fatalf("ranked rhythm difficulties differ: %v", modes)
	}
	info := h.call(t, "/MiniGameRhythmInfo", testRow(1, 7, 2, 150))
	best := map[uint64]uint64{}
	_ = wire.Walk(info, func(f wire.Field) error {
		if f.Number == 1 {
			best[num(f.Value, 2)] = num(f.Value, 3)
		}
		return nil
	})
	if best[0] != 800 || best[1] != 500 || best[2] != 700 {
		t.Fatal("rhythm info lost per-mode records")
	}
}

type retainedActionRoom struct {
	run    NativeRun
	result NativeResult
}

func (r *retainedActionRoom) StartRun(command.Context, string) (NativeRun, error) { return r.run, nil }
func (r *retainedActionRoom) Result(command.Context, NativeRun) (NativeResult, error) {
	return r.result, nil
}

func TestNativeActionMissionsUseRoomClearAndGrantOnlyOnce(t *testing.T) {
	h := newPlayHarness(t)
	h.ctx.Emit = func(command.Event) error { return nil }
	h.design.Tables["PackEventMiniGameTable"] = append(h.design.Tables["PackEventMiniGameTable"], testRow(8, 60, 3, 8, 4, 1))
	if err := h.registry.Replace(append(h.registry.List(), events.Schedule{UID: 160, Type: 11, ID: 60, Start: h.now.UnixMilli() - 1000, End: h.now.UnixMilli() + 86400000})); err != nil {
		t.Fatal(err)
	}
	h.design.Tables["ActionGameDefaultTable"] = [][]byte{testRow(1, 1)}
	h.design.Tables["ActionGameStageTable"] = [][]byte{testRow(3, 1, 4, 1, 1, 101, 11, 1)}
	h.design.Tables["ActionGameCharTable"] = [][]byte{testRow(14, 101, 16, 1)}
	h.design.Tables["ActionGameMissionTable"] = [][]byte{testRow(4, 1, 3, 1, 1, 101, 7, 1, 8, 1, 9, 99, 10, 27), testRow(4, 2, 3, 2, 1, 3, 2, 101, 7, 1, 8, 2, 9, 98, 10, 27), testRow(4, 3, 3, 3, 1, 2, 2, 101, 7, 1), testRow(4, 4, 3, 11, 1, 101, 7, 1), testRow(4, 5, 3, 6, 1, 2, 2, 101, 7, 1, 8, 1, 9, 97, 10, 27)}
	room := &retainedActionRoom{run: NativeRun{UID: 160, Family: "Action", Group: 1, Stage: 1, Monster: 101, InstanceID: "first-room", GUID: "first-guid", Started: h.now.UnixMilli()}, result: NativeResult{Clear: false, Score: 2000, Evidence: wire.AppendBytes(nil, 2, testRow(1, 7, 2, 2))}}
	h.service.AttachRooms(room)
	end := func(seq uint64) []byte {
		return wire.AppendString(testRow(1, seq, 3, 1, 4, 1, 6, 101, 7, 999999, 22, 1, 8, 1, 11, 2), 2, room.run.GUID)
	}
	h.call(t, "/MiniGameActionStart", testRow(1, 1))
	h.call(t, "/MiniGameActionEnd", end(2))
	if h.balance(t, 27) != 0 {
		t.Fatal("submitted clear granted mission after authoritative room failure")
	}
	room.run.InstanceID, room.run.GUID = "second-room", "second-guid"
	room.result.Clear = true
	h.call(t, "/MiniGameActionStart", testRow(1, 3))
	response := h.call(t, "/MiniGameActionEnd", end(4))
	if h.balance(t, 27) != 3 {
		t.Fatal("authoritative fast clear missions did not grant actual rewards")
	}
	if num(response, 1) != 2000 {
		t.Fatal("HTTP score replaced authoritative room time")
	}
	h.reopen(t)
	h.service.AttachRooms(room)
	if !bytes.Equal(response, h.call(t, "/MiniGameActionEnd", end(4))) || h.balance(t, 27) != 3 {
		t.Fatal("native mission retry duplicated reward")
	}
	info := h.call(t, "/MiniGameActionInfo", testRow(1, 5, 2, 160))
	missions, _ := list(info, 3)
	if len(missions) != 4 {
		t.Fatal("mission ledger did not survive reopen")
	}
	best, _, _ := wire.Bytes(info, 2)
	if num(best, 1) != 101 || num(best, 2) != 2000 {
		t.Fatal("Action Info did not key best by monster")
	}
	room.run.InstanceID, room.run.GUID = "third-room", "third-guid"
	room.result.Evidence = wire.AppendBytes(nil, 2, testRow(1, 7, 2, 1))
	h.call(t, "/MiniGameActionStart", testRow(1, 6))
	h.call(t, "/MiniGameActionEnd", end(7))
	if h.balance(t, 27) != 4 {
		t.Fatal("new room failed to award only the own-character mission")
	}
}

func TestSurvivalEvolutionRequiresOwnedAbilityAndFallbackUpdatesWallet(t *testing.T) {
	h := newPlayHarness(t)
	h.design.Tables["FieldMiniGameSkillGroupTable"][0] = testRow(1, 1, 2, 2, 5, 1, 7, 1, 8, 2)
	h.design.Tables["FieldMiniGameSkillGroupTable"] = append(h.design.Tables["FieldMiniGameSkillGroupTable"], testRow(1, 8, 2, 1, 4, 2002))
	h.design.Tables["FieldMiniGameSkillTable"] = append(h.design.Tables["FieldMiniGameSkillTable"], wire.AppendDouble(testRow(7, 8, 8, 1), 17, 40))
	h.design.Tables["FieldMiniGameSurvivalItemTable"] = append(h.design.Tables["FieldMiniGameSurvivalItemTable"], testRow(1, 13, 3, 5, 4, 1, 6, 100))
	h.design.Tables["FieldMiniGameSurvivalBoxTable"][0] = testRow(2, 1, 3, 11, 3, 12, 3, 13)
	h.call(t, "/MiniGameSurvivalStart", testRow(1, 1, 2, 110, 3, 1, 4, 1, 5, 1))
	h.now = h.now.Add(time.Second)
	play := testRow(1, 2, 2, 100, 3, 10, 4, 5, 7, 1)
	for _, id := range []uint64{11, 12, 13} {
		play = wire.AppendBytes(play, 5, testRow(1, id, 2, 1))
	}
	play = wire.AppendBytes(play, 6, testRow(1, 1, 2, 1))
	h.call(t, "/MiniGameSurvivalPlay", play)
	offers := h.call(t, "/MiniGameSurvivalSkillSelectList", testRow(1, 3))
	ids, _ := list(offers, 2)
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatal("weapon evolved before its combination ability was acquired")
	}
	h.call(t, "/MiniGameSurvivalSkillUp", testRow(1, 4, 2, 2))
	h.call(t, "/MiniGameSurvivalSkillUp", testRow(1, 5, 3, 13))
	h.now = h.now.Add(time.Second)
	play = testRow(1, 6, 2, 100, 3, 30, 4, 15, 7, 1, 7, 1, 7, 1)
	for _, id := range []uint64{11, 12, 13} {
		play = wire.AppendBytes(play, 5, testRow(1, id, 2, 3))
	}
	play = wire.AppendBytes(play, 6, testRow(1, 1, 2, 3))
	h.call(t, "/MiniGameSurvivalPlay", play)
	for seq := uint64(7); seq < 10; seq++ {
		h.call(t, "/MiniGameSurvivalSkillUp", testRow(1, seq, 3, 13))
	}
	if h.service.state.Runs["Survival"].Coin != 100 {
		t.Fatal("fully upgraded chest did not issue coin fallback")
	}
	h.call(t, "/MiniGameSurvivalEnd", testRow(1, 10, 2, 1, 3, 2, 4, 4, 5, 100, 6, 40))
	if h.balance(t, 43) != 100 {
		t.Fatal("fallback coins were not credited at End")
	}
}
