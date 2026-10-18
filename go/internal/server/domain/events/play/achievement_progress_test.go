package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/protocol/wire"
	"reflect"
	"testing"
)

func TestRhythmAchievementsRequireCompletedHardAndAllChallengeSongs(t *testing.T) {
	h := newPlayHarness(t)
	h.design.Tables["RhythmGameMusicTable"] = [][]byte{
		testRow(5, 1, 8, 30000, 2, 30000, 1, 30000, 10, 1, 4, 1),
		testRow(5, 2, 8, 30000, 2, 30000, 1, 30000, 10, 1, 4, 1),
	}
	h.design.Tables["RhythmGameCharTable"] = [][]byte{testRow(2, 1)}
	// GameData 20261012190548 groups 2006/2014 require a HARD clear/all CHALLENGE
	// songs; groups 2023/2024 require a score of 30000/50000 in any difficulty.
	design := &gamedata.AchievementCounterDesign{Conditions: map[int]gamedata.AchievementCondition{
		2006: {Type: 125, SubType: 1}, 2014: {Type: 125, SubType: 2},
		2023: {Type: 156, SubType: 30000}, 2024: {Type: 156, SubType: 50000},
	}}
	play := func(seq, music, mode, score uint64, checksum string, gameOver bool) {
		t.Helper()
		h.call(t, "/MiniGameRhythmPlayStart", testRow(1, seq, 2, 150, 3, music, 4, 1, 5, mode))
		grade := uint64(1)
		if gameOver {
			grade = 6
		}
		end := wire.AppendString(testRow(1, seq+1, 2, grade, 4, score), 5, checksum)
		if gameOver {
			end = wire.AppendVarint(end, 7, 1)
		} else {
			end = wire.AppendVarint(end, 6, 2)
			end = wire.AppendBytes(end, 3, testRow(1, 1, 2, 1))
		}
		h.call(t, "/MiniGameRhythmPlayEnd", end)
	}
	// Four GetRandom(11111, 99999) calls on the official 2.36.7 DLL's Well512
	// with seeds 30010, 30012, 13, and 30016 supply these independent game IDs.
	play(1, 1, 1, 30000, "17242-77482-23177-94113", false)
	play(3, 1, 2, 30000, "60962-43922-45105-49409", false)
	play(5, 2, 2, 0, "35103-26159-36575-57735", true)
	h.reopen(t)
	progress, err := h.service.AchievementProgress(design)
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]uint64]uint64{{125, 1}: 1, {125, 2}: 0, {156, 30000}: 1, {156, 50000}: 0}
	if !reflect.DeepEqual(progress, want) {
		t.Fatalf("quit awarded all-song achievement or completed score was lost: %v", progress)
	}
	play(7, 2, 2, 30000, "89338-96162-28825-41345", false)
	progress, err = h.service.AchievementProgress(design)
	if err != nil {
		t.Fatal(err)
	}
	want[[2]uint64{125, 2}] = 1
	if !reflect.DeepEqual(progress, want) {
		t.Fatalf("all-song challenge achievement stayed stale after settlement: %v", progress)
	}
	h.reopen(t)
	progress, err = h.service.AchievementProgress(design)
	if err != nil || !reflect.DeepEqual(progress, want) {
		t.Fatalf("all-song challenge achievement did not survive login: %v, %v", progress, err)
	}
}

func TestHopscotchAchievementsCountClearsWithoutCountingPartialRunsOrRetries(t *testing.T) {
	h := newPlayHarness(t)
	h.design.Tables["HopscotchDefaultTable"] = [][]byte{testRow(4, 80, 9, 300)}
	h.design.Tables["HopscotchStageTable"] = [][]byte{testRow(6, 1)}
	// GameData 20261012190548 groups 2025..2028 count clear/full-completion/
	// no-continue clears; a partial run after an earlier clear proves no new clear.
	design := &gamedata.AchievementCounterDesign{Conditions: map[int]gamedata.AchievementCondition{
		2025: {Type: 158}, 2026: {Type: 159}, 2027: {Type: 160},
	}}
	play := func(seq, area, elapsed, continued uint64) []byte {
		t.Helper()
		h.call(t, "/MiniGameHopscotchGameStart", testRow(1, seq, 2, 130, 3, 1))
		end := testRow(1, seq+1, 2, area, 3, elapsed, 4, continued)
		h.call(t, "/MiniGameHopscotchGameEnd", end)
		return end
	}
	play(1, 85, 4000, 1)
	play(3, 100, 5000, 0)
	partial := play(5, 40, 6000, 0)
	h.reopen(t)
	h.call(t, "/MiniGameHopscotchGameEnd", partial)
	progress, err := h.service.AchievementProgress(design)
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]uint64]uint64{{158, 0}: 2, {159, 0}: 1, {160, 0}: 1}
	if !reflect.DeepEqual(progress, want) {
		t.Fatalf("partial run or retry changed achieved clear counts: %v", progress)
	}
	play(7, 80, 4500, 0)
	progress, err = h.service.AchievementProgress(design)
	if err != nil {
		t.Fatal(err)
	}
	want[[2]uint64{158, 0}], want[[2]uint64{160, 0}] = 3, 2
	if !reflect.DeepEqual(progress, want) {
		t.Fatalf("repeated clear of the same stage was lost: %v", progress)
	}
	h.reopen(t)
	progress, err = h.service.AchievementProgress(design)
	if err != nil || !reflect.DeepEqual(progress, want) {
		t.Fatalf("repeated clear progress did not survive login: %v, %v", progress, err)
	}
}
