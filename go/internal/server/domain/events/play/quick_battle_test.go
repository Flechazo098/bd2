package eventplay

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	readonly "bd2server/internal/server/protocol/staticdata"
	"bd2server/internal/server/protocol/wire"
)

func setupBattleSweep(t *testing.T) *playHarness {
	t.Helper()
	h := newPlayHarness(t)
	if err := h.registry.Replace([]events.Schedule{{UID: 90, Type: 9, ID: 900, Start: h.now.UnixMilli() - 1000, End: h.now.UnixMilli() + 100000}}); err != nil {
		t.Fatal(err)
	}
	h.design.Tables["PackEventBattleTable"] = [][]byte{testRow(1, 800, 3, 2, 4, 900, 5, 1, 7, 1, 8, 10, 9, 8, 10, 50)}
	h.design.Tables["PackEventBattleGroupTable"] = [][]byte{testRow(3, 900)}
	var rate [4]byte
	binary.LittleEndian.PutUint32(rate[:], math.Float32bits(.5))
	h.design.Tables["PickUpCostumeEventTable"] = [][]byte{wire.AppendBytes(testRow(2, 777, 3, 3, 4, 1, 5, 50, 6, 8), 1, rate[:])}
	raw, _ := json.Marshal(map[uint64]uint64{30: 100})
	if err := h.ctx.State.Save("test-wallet", raw); err != nil {
		t.Fatal(err)
	}
	attachSweepRules(h)
	return h
}

func attachSweepRules(h *playHarness) {
	h.service.AttachBattleChallenges(gamedata.EventBattleChallenges{800: {
		{Type: 1, Reward: gamedata.BattleReward{Type: 4, Count: 100}},
		{Type: 2, Reward: gamedata.BattleReward{Type: 4, Count: 20}},
	}})
	h.service.AttachBattleCostumes(func(command.Context) map[uint64]bool { return map[uint64]bool{777: true} })
	h.service.AttachHubCalendars(&readonly.Seed{Responses: map[string]readonly.Response{"/EventHubInfo": {PacketCode: 222, Fields: []readonly.Field{
		{Number: 1, Type: 2, Fields: []readonly.Field{
			{Number: 1, Type: 0, Varint: 1000}, {Number: 2, Type: 0, Varint: 3},
			{Number: 3, Type: 0, Varint: 999999000}, {Number: 4, Type: 0, Varint: 1000100000}, {Number: 5, Type: 0, Varint: 1000200000},
			{Number: 6, Type: 2, Fields: []readonly.Field{{Number: 1, Type: 0, Varint: 1}, {Number: 2, Type: 0, Varint: 2}, {Number: 3, Type: 0, Varint: 90}}},
		}},
	}}}})
}

func clearSweepBattle(t *testing.T, h *playHarness, receipt string, challenges ...uint64) {
	t.Helper()
	if _, err := h.service.EnterBattle(h.ctx, testRow(4, 800, 5, 17, 8, 900, 9, 1), receipt); err != nil {
		t.Fatal(err)
	}
	end := testRow(2, 1)
	for _, index := range challenges {
		end = wire.AppendVarint(end, 5, index)
	}
	if _, err := h.service.CompleteBattle(h.ctx, end, receipt); err != nil {
		t.Fatal(err)
	}
}

func TestEventBattleSweepRequiresAllChallengesAndRestoresPaidRewards(t *testing.T) {
	h := setupBattleSweep(t)
	req := testRow(1, 5, 2, 17, 3, 800, 4, 900, 5, 1, 6, 3)
	h.reject(t, "/QuickBattle", req)
	clearSweepBattle(t, h, "login:1", 0)
	h.reject(t, "/QuickBattle", req)
	if h.balance(t, 30) != 98 || h.balance(t, 8) != 15 || h.balance(t, 4) != 100 {
		t.Fatal("normal clear did not debit AP and grant repeat/first challenge rewards with owned costume bonus")
	}
	clearSweepBattle(t, h, "login:2", 1)
	first := h.call(t, "/QuickBattle", req)
	if h.balance(t, 30) != 90 || h.balance(t, 8) != 75 || h.balance(t, 4) != 120 {
		t.Fatal("sweep costs/repeat multiplier or one-time challenge reward is incorrect")
	}
	h.reopen(t)
	attachSweepRules(h)
	if !bytes.Equal(first, h.call(t, "/QuickBattle", req)) || h.balance(t, 30) != 90 || h.balance(t, 8) != 75 {
		t.Fatal("reopened sweep retry charged or granted twice")
	}
	h.call(t, "/QuickBattle", testRow(1, 6, 2, 17, 3, 800, 4, 900, 5, 1, 6, 1))
	if h.balance(t, 30) != 88 || h.balance(t, 8) != 90 || h.balance(t, 4) != 120 {
		t.Fatal("new sweep did not debit and grant exactly one repeat")
	}
}

func TestEventBattleSweepRejectsIdentityCostsAndClosedWindowsWithoutAssetChanges(t *testing.T) {
	h := setupBattleSweep(t)
	clearSweepBattle(t, h, "login:1", 0, 1)
	before, _ := h.ctx.State.Load("test-wallet")
	for _, req := range [][]byte{
		testRow(1, 2, 2, 17, 3, 801, 4, 900, 5, 1, 6, 1),
		testRow(1, 2, 2, 17, 3, 800, 4, 900, 5, 2, 6, 1),
		testRow(1, 2, 2, 17, 3, 800, 4, 901, 5, 1, 6, 1),
		testRow(1, 2, 2, 1, 3, 800, 4, 900, 5, 1, 6, 1),
		testRow(1, 2, 2, 17, 3, 800, 4, 900, 5, 1, 6, 0),
		testRow(1, 2, 2, 17, 3, 800, 4, 900, 5, 1, 6, 101),
		testRow(1, 2, 2, 17, 3, 800, 4, 900, 5, 1, 6, 50),
		wire.AppendVarint(testRow(1, 2, 2, 17, 3, 800, 4, 900, 5, 1, 6, 1), 6, 1),
	} {
		h.reject(t, "/QuickBattle", req)
	}
	row := h.design.Tables["PackEventBattleTable"][0]
	h.design.Tables["PackEventBattleTable"][0] = testRow(1, 800, 3, 2, 4, 900, 5, 1, 8, 10, 9, 8, 10, 50)
	h.reject(t, "/QuickBattle", testRow(1, 2, 2, 17, 3, 800, 4, 900, 5, 1, 6, 1))
	h.design.Tables["PackEventBattleTable"][0] = row
	h.now = h.now.Add(2 * time.Minute)
	h.reject(t, "/QuickBattle", testRow(1, 2, 2, 17, 3, 800, 4, 900, 5, 1, 6, 1))
	after, _ := h.ctx.State.Load("test-wallet")
	if !bytes.Equal(before, after) {
		t.Fatal("rejected sweep changed owned assets")
	}
}
