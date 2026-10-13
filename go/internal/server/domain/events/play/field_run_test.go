package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"fmt"
	"testing"
	"time"
)

func fieldRunHarness(t *testing.T) *playHarness {
	t.Helper()
	h := newPlayHarness(t)
	h.design.Tables["PackEventMiniGameTable"] = append(h.design.Tables["PackEventMiniGameTable"], testRow(8, 90, 3, 2, 10, 90), testRow(8, 91, 3, 3, 10, 91), testRow(8, 92, 3, 4, 10, 92))
	h.design.Tables["FieldMiniGameRewardTable"] = [][]byte{
		testRow(1, 90, 2, 1, 3, 100, 6, 8, 5, 77, 4, 10), testRow(1, 90, 2, 2, 3, 200, 6, 4, 5, 0, 4, 20),
		testRow(1, 91, 2, 1, 3, 45, 6, 4, 5, 0, 4, 20), testRow(1, 91, 2, 2, 3, 35, 6, 8, 5, 77, 4, 60), testRow(1, 91, 2, 3, 3, 25, 6, 4, 5, 0, 4, 30),
		testRow(1, 92, 2, 1, 3, 5, 6, 4, 5, 0, 4, 20), testRow(1, 92, 2, 2, 3, 10, 6, 8, 5, 77, 4, 10),
	}
	calendars := h.registry.List()
	for _, game := range []uint64{90, 91, 92} {
		calendars = append(calendars, events.Schedule{UID: game + 100, Type: 11, ID: game, Start: h.now.UnixMilli() - 1000, End: h.now.Add(48 * time.Hour).UnixMilli()})
	}
	if err := h.registry.Replace(calendars); err != nil {
		t.Fatal(err)
	}
	h.service.fieldRules = func(uint64) (*gamedata.EventField, error) {
		return &gamedata.EventField{HP: 300, Limit: 3600, RandomCounts: map[uint64]uint64{7: 1}, Objects: map[uint64]gamedata.FieldGameObject{
			1: {Point: 200, Type: 1}, 2: {Point: -50, Type: 1}, 3: {Point: 100, Type: 2}, 4: {Point: -100, Type: 2}, 5: {Point: 100, Type: 1, RandomGroup: 7}, 6: {Point: 100, Type: 1, RandomGroup: 7},
		}}, nil
	}
	return h
}

// RacingHUD applies signed score changes, HP-point capped to starting HP, and
// only the start response's enabled objects. EventMiniGameUI's reward guide
// accumulates every reached row, rather than replacing one row with another.
func TestRacingSelectedObjectsHealthAndCumulativeDailyRewards(t *testing.T) {
	h := fieldRunHarness(t)
	start := testRow(1, 1, 2, 190)
	body := h.call(t, "/MiniGameFieldStart", start)
	ids, err := list(body, 1)
	if err != nil || len(ids) != 5 {
		t.Fatalf("enabled objects: %v %v", ids, err)
	}
	if !bytes.Equal(body, h.call(t, "/MiniGameFieldStart", start)) {
		t.Fatal("start retry resampled objects")
	}
	inactive := uint64(5)
	if has(ids, 5) {
		inactive = 6
	}
	h.reject(t, "/MiniGameFieldScore", testRow(1, 2, 2, 190, 3, inactive))
	result := h.call(t, "/MiniGameFieldScore", testRow(1, 3, 2, 190, 3, 1, 3, 2, 3, 3))
	if num(result, 1) != 150 || num(result, 2) != 200 {
		t.Fatal("signed score or HP damage diverges from RacingHUD")
	}
	h.reject(t, "/MiniGameFieldScore", testRow(1, 4, 2, 191, 3, 1))
	result = h.call(t, "/MiniGameFieldScore", testRow(1, 5, 2, 190, 3, 4))
	if num(result, 2) != 300 {
		t.Fatal("healing did not cap at starting health")
	}
	end := testRow(1, 6)
	h.call(t, "/MiniGameFieldEnd", end)
	if h.balance(t, 8) != 10 || h.balance(t, 4) != 0 {
		t.Fatal("first daily tier paid wrong inventory")
	}
	h.call(t, "/MiniGameFieldEnd", end)
	if h.balance(t, 8) != 10 {
		t.Fatal("end retry paid twice")
	}
	h.call(t, "/MiniGameFieldStart", testRow(1, 7, 2, 190))
	h.call(t, "/MiniGameFieldScore", testRow(1, 8, 2, 190, 3, 1, 3, 1))
	h.call(t, "/MiniGameFieldEnd", testRow(1, 9))
	if h.balance(t, 8) != 10 || h.balance(t, 4) != 20 {
		t.Fatal("improved score did not pay exactly the additional tier")
	}
	h.reopen(t)
	h.now = h.now.Add(24 * time.Hour)
	body = h.call(t, "/MiniGameFieldInfo", testRow(1, 10, 2, 190))
	info, _, err := wire.Bytes(body, 1)
	if err != nil || num(info, 3) != 400 || num(info, 2) != 0 || num(info, 4) != 1 {
		t.Fatal("daily reset lost season best or did not enable quick reward")
	}
	h.call(t, "/MiniGameFieldQuickReward", testRow(1, 11, 2, 190))
	h.reopen(t)
	h.reject(t, "/MiniGameFieldQuickReward", testRow(1, 12, 2, 190))
	if h.balance(t, 8) != 20 || h.balance(t, 4) != 40 {
		t.Fatal("next-day cumulative reward was lost or duplicated across reopening")
	}
}

func runHistory(seconds float64) string {
	return fmt.Sprintf(`{"TotalTime":%g,"TimelineDatas":[{"Time":0,"Behaviour":1,"Position":{"x":0,"y":0,"z":0},"SpeedMagnification":1},{"Time":%g,"Behaviour":7,"Position":{"x":1,"y":0,"z":1},"SpeedMagnification":0}]}`, seconds, seconds)
}

// RunHUD reports milliseconds plus RunPlayerController's completed ghost.
// The reward guide marks time <= authored seconds as earned for Run, and
// time >= authored seconds for Dodge. These exercise settlement and reopening.
func TestRunFastestGhostAndDodgeSurvivalRewardDirections(t *testing.T) {
	h := fieldRunHarness(t)
	for i, seconds := range []float64{40, 30, 50} {
		h.call(t, "/MiniGameRunStart", testRow(1, uint64(i*3+1), 2, 191))
		h.now = h.now.Add(time.Duration(seconds) * time.Second)
		end := wire.AppendString(testRow(1, uint64(i*3+2), 3, uint64(seconds*1000)), 4, runHistory(seconds))
		result := h.call(t, "/MiniGameRunEnd", end)
		if i == 2 && num(result, 1) != 30000 {
			t.Fatal("slower run replaced fastest completed record")
		}
		h.call(t, "/MiniGameRunEnd", end)
		h.reopen(t)
	}
	if h.balance(t, 4) != 20 || h.balance(t, 8) != 60 {
		t.Fatal("run tiers were not cumulative or repeated/slower runs paid again")
	}
	body := h.call(t, "/MiniGameRunInfo", testRow(1, 11, 2, 191))
	info, _, err := wire.Bytes(body, 1)
	history, _, historyErr := wire.Bytes(info, 8)
	if err != nil || historyErr != nil || num(info, 7) != 30000 || string(history) != runHistory(30) {
		t.Fatal("world ghost differs from the fastest persisted result")
	}
	h.call(t, "/MiniGameRunStart", testRow(1, 12, 2, 191))
	h.now = h.now.Add(time.Second)
	h.reject(t, "/MiniGameRunEnd", wire.AppendString(testRow(1, 13, 3, 1000), 4, runHistory(40)))
	h.call(t, "/MiniGameRunEnd", testRow(1, 14, 2, 1, 3, 1000))
	if h.balance(t, 4) != 20 || h.balance(t, 8) != 60 {
		t.Fatal("abandoned run granted rewards")
	}
	h.service.fieldRules = func(uint64) (*gamedata.EventField, error) { return &gamedata.EventField{Limit: 3600}, nil }
	for i, seconds := range []uint64{5, 12, 7} {
		h.call(t, "/MiniGameRunStart", testRow(1, uint64(20+i*3), 2, 192))
		h.now = h.now.Add(time.Duration(seconds) * time.Second)
		result := h.call(t, "/MiniGameRunEnd", testRow(1, uint64(21+i*3), 3, seconds*1000))
		if i == 2 && num(result, 1) != 12000 {
			t.Fatal("shorter dodge survival replaced best record")
		}
	}
	if h.balance(t, 4) != 40 || h.balance(t, 8) != 70 {
		t.Fatal("dodge time units or additional-tier payments wrong")
	}
}
