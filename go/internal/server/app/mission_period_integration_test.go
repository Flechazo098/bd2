//go:build integration

package app

import (
	"bd2server/internal/server/domain/progression/conditions"
	"context"
	"fmt"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
)

type missionGameplay struct {
	t       *testing.T
	factory *PlayerFactory
	account string
	owner   *playerInstance
	now     time.Time
	seq     uint64
}

func newMissionGameplay(t *testing.T) *missionGameplay {
	t.Helper()
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	days := (8 - int(now.Weekday())) % 7
	g := &missionGameplay{t: t, factory: factory, account: accounts[0], owner: owner, now: time.Date(now.Year(), now.Month(), now.Day()+days, 12, 0, 0, 0, time.UTC)}
	owner.assembly.missionService.SetClock(func() time.Time { return g.now })
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return g
}

func (g *missionGameplay) execute(path string, fields []byte) ([]byte, error) {
	g.seq++
	request := registryCommand(g.account, fmt.Sprintf("mission-%d", g.seq), path, append(appScalar(1, g.seq), fields...))
	if path == "/LoginUser" {
		request.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
	}
	reply, err := g.owner.Execute(context.Background(), request)
	if err != nil {
		return nil, err
	}
	if len(reply.Responses) != 1 {
		return nil, fmt.Errorf("%s returned %d responses", path, len(reply.Responses))
	}
	return reply.Responses[0].Body, nil
}

func (g *missionGameplay) call(path string, fields []byte) []byte {
	g.t.Helper()
	response, err := g.execute(path, fields)
	if err != nil {
		g.t.Fatalf("%s: %v", path, err)
	}
	return response
}

func (g *missionGameplay) reopen() {
	g.t.Helper()
	if err := g.owner.Close(); err != nil {
		g.t.Fatal(err)
	}
	g.owner = nil
	var err error
	g.owner, err = g.factory.open(g.account)
	if err != nil {
		g.t.Fatal(err)
	}
	g.owner.assembly.missionService.SetClock(func() time.Time { return g.now })
}

func (g *missionGameplay) wallet() assets.Currency {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	return g.owner.assembly.wallet.Snapshot(command.Context{State: tx})
}

func (g *missionGameplay) itemCount(typ, id uint64) uint64 {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var count uint64
	for _, item := range g.owner.assembly.ownedItems.All(command.Context{State: tx}) {
		if item.Type == typ && item.ID == id {
			count += item.Count
		}
	}
	return count
}

func (g *missionGameplay) assertMission(groupType, group, id, value uint64, claimed bool) {
	g.t.Helper()
	var row []byte
	found := false
	for _, candidate := range appRows(g.call("/MissionInfo", nil), 1) {
		if appValue(candidate, 1) == group && appValue(candidate, 2) == id && appValue(candidate, 3) == groupType {
			row, found = candidate, true
		}
	}
	if !found {
		g.t.Fatalf("mission %d/%d/%d missing from MissionInfo", groupType, group, id)
	}
	if appValue(row, 4) != value || (appValue(row, 5) == 1) != claimed {
		g.t.Fatalf("mission %d/%d/%d: value=%d claimed=%v, want value=%d claimed=%v", groupType, group, id, appValue(row, 4), appValue(row, 5) == 1, value, claimed)
	}
}

func (g *missionGameplay) claim(all bool, groupType, group, id uint64) []byte {
	fields := append(appScalar(3, groupType), appScalar(4, group)...)
	fields = append(fields, appScalar(5, id)...)
	if all {
		fields = append(fields, appScalar(2, 1)...)
	}
	return g.call("/MissionClear", fields)
}

func missionRewardCount(t *testing.T, response []byte, typ, id uint64) uint64 {
	t.Helper()
	bundles := appRows(response, 1)
	if len(bundles) != 1 {
		t.Fatalf("MissionClear must return one reward envelope, got %d", len(bundles))
	}
	var total uint64
	for _, row := range appRows(bundles[0], 1) {
		if appValue(row, 3) == typ && appValue(row, 2) == id {
			total += appValue(row, 4)
		}
	}
	return total
}

// Current client MissionUI bulk requests select group_type 0 (daily) or 1
// (weekly), with group/id omitted. GameData 20261008155037 and LocalText
// 5500001/5501001 specify one login per day: daily 1/101 gives 4000 gold;
// weekly 2/201 requires five days and gives 50000. Daily 1/110 gives box
// 504145 x 1; weekly 2/210 counts five completed group-1 missions, giving x 4.
func TestMissionLoginClaimsKeepDailyAndWeeklyPeriodsSeparate(t *testing.T) {
	g := newMissionGameplay(t)
	start := g.now
	before := g.wallet()
	boxes := g.itemCount(9, 504145)
	for day := range 5 {
		g.now = start.AddDate(0, 0, day)
		g.call("/LoginUser", nil)
		g.call("/LoginUser", nil)
		g.assertMission(0, 1, 101, 1, false)
		g.assertMission(1, 2, 201, uint64(day+1), false)
		g.assertMission(1, 2, 210, min(uint64(2*(day+1)), uint64(5)), false)
		var response []byte
		if day == 0 {
			response = g.claim(false, 0, 1, 101)
			g.claim(false, 0, 1, 110)
		} else {
			response = g.claim(true, 0, 0, 0)
		}
		if missionRewardCount(t, response, 4, 0) != 4000 || g.wallet().Gold != before.Gold+uint64(day+1)*4000 || g.itemCount(9, 504145) != boxes+uint64(day+1) {
			t.Fatalf("daily day %d did not grant exactly one daily gold/box reward", day+1)
		}
		g.assertMission(1, 2, 201, uint64(day+1), false)
		g.assertMission(1, 2, 210, min(uint64(2*(day+1)), uint64(5)), false)
	}
	g.reopen()
	g.call("/LoginUser", nil)
	g.assertMission(1, 2, 201, 5, false)
	g.now = start.AddDate(0, 0, 5)
	g.call("/LoginUser", nil)
	weekly := g.claim(true, 1, 0, 0)
	if missionRewardCount(t, weekly, 4, 0) != 50000 || g.wallet().Gold != before.Gold+70000 || g.itemCount(9, 504145) != boxes+9 {
		t.Fatal("weekly bulk did not grant only its 50000 gold and four boxes")
	}
	g.assertMission(0, 1, 101, 1, false)
	g.assertMission(1, 2, 201, 5, true)
	g.assertMission(1, 2, 210, 5, true)
	g.claim(true, 0, 0, 0)
	g.call("/MissionSectionReward", append(appScalar(2, 0), appScalar(3, 1)...))
	if g.wallet().Gold != before.Gold+74000 || g.wallet().Catalyst != before.Catalyst+70 || g.itemCount(9, 504145) != boxes+10 {
		t.Fatal("daily rewards or first section were consumed by the weekly claim")
	}
	retry := g.claim(false, 1, 2, 201)
	if missionRewardCount(t, retry, 4, 0) != 0 || g.wallet().Gold != before.Gold+74000 {
		t.Fatal("a new request for an already received weekly reward echoed or paid gold")
	}
	g.reopen()
	if g.wallet().Gold != before.Gold+74000 || g.itemCount(9, 504145) != boxes+10 {
		t.Fatal("reopening SQLite lost granted mission rewards")
	}
	for offset := range 5 {
		day := offset + 7
		g.now = start.AddDate(0, 0, day)
		g.call("/LoginUser", nil)
		g.assertMission(1, 2, 201, uint64(day-6), false)
		g.claim(true, 0, 0, 0)
		if day == 7 {
			g.call("/MissionSectionReward", append(appScalar(2, 0), appScalar(3, 1)...))
		}
	}
	g.claim(false, 1, 2, 201)
	g.claim(true, 1, 2, 0)
	if g.wallet().Gold != before.Gold+144000 || g.wallet().Catalyst != before.Catalyst+140 || g.itemCount(9, 504145) != boxes+19 {
		t.Fatal("new daily/weekly periods did not pay the newly earned currency and items")
	}
	g.assertMission(1, 2, 201, 5, true)
	g.assertMission(1, 2, 210, 5, true)
}

func (g *missionGameplay) completeFixtures(keys []gamedata.MissionKey) {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{State: tx}
	for _, key := range keys {
		if err := g.owner.assembly.missionService.CompleteMission(ctx, key); err != nil {
			g.t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		g.t.Fatal(err)
	}
}

// Section rewards count claimed tasks in their selected tab. Independent
// current GameData rows: daily 101/102/103 give catalyst70, box504153 x1,
// diamonds60 at 1/3/5 claims; weekly 201 gives box504144 x4 at three claims.
func TestMissionSectionsStayInSelectedPeriodAndPayAgainNextWeek(t *testing.T) {
	g := newMissionGameplay(t)
	before := g.wallet()
	dailyBoxes, weeklyBoxes := g.itemCount(9, 504153), g.itemCount(9, 504144)
	keys := []gamedata.MissionKey{{GroupType: 0, GroupID: 1, ID: 101}, {GroupType: 0, GroupID: 1, ID: 109}, {GroupType: 0, GroupID: 1, ID: 111}, {GroupType: 0, GroupID: 1, ID: 113}, {GroupType: 1, GroupID: 2, ID: 201}, {GroupType: 1, GroupID: 2, ID: 218}, {GroupType: 1, GroupID: 2, ID: 219}}
	for offset := range 2 {
		period := offset + 1
		g.call("/LoginUser", nil)
		g.completeFixtures(keys)
		g.claim(true, 0, 0, 0)
		g.claim(true, 1, 0, 0)
		response := g.call("/MissionSectionReward", appScalar(3, 1))
		var catalyst uint64
		for _, row := range appRows(response, 1) {
			if appValue(row, 3) == 12 {
				catalyst += appValue(row, 4)
			}
		}
		if catalyst != 70 || g.wallet().Catalyst != before.Catalyst+uint64(period)*70 || g.wallet().FreeJewelry != before.FreeJewelry+uint64(period)*60 || g.itemCount(9, 504153) != dailyBoxes+uint64(period) || g.itemCount(9, 504144) != weeklyBoxes+uint64(period-1)*4 {
			t.Fatal("daily section bulk mixed weekly rewards or failed to grant its current rewards")
		}
		g.call("/MissionSectionReward", append(appScalar(2, 1), appScalar(4, 201)...))
		if g.itemCount(9, 504144) != weeklyBoxes+uint64(period)*4 {
			t.Fatal("weekly section did not remain independently claimable")
		}
		g.reopen()
		g.call("/MissionSectionReward", append(appScalar(2, 1), appScalar(3, 1)...))
		g.call("/MissionSectionReward", appScalar(3, 1))
		if g.wallet().Catalyst != before.Catalyst+uint64(period)*70 || g.wallet().FreeJewelry != before.FreeJewelry+uint64(period)*60 || g.itemCount(9, 504153) != dailyBoxes+uint64(period) || g.itemCount(9, 504144) != weeklyBoxes+uint64(period)*4 {
			t.Fatal("section retries after reopening SQLite duplicated rewards")
		}
		g.now = g.now.AddDate(0, 0, 7)
	}
}

// ChangeMissionTable 1/104 ended 2025-12-08; its former one-collection
// condition and 200-gold reward must not receive current gameplay events.
func TestMissionExpiredRowCannotReceiveCurrentGameplayEvents(t *testing.T) {
	g := newMissionGameplay(t)
	g.call("/LoginUser", nil)
	before := g.wallet()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{State: tx}
	if err := g.owner.assembly.missionService.RecordEvent(ctx, conditions.Event{Type: 3, Count: 1}, g.owner.assembly.worldService.MissionsUnlocked); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, row := range appRows(g.call("/MissionInfo", nil), 1) {
		if appValue(row, 1) == 1 && appValue(row, 2) == 104 {
			t.Fatal("current collection event completed a mission already hidden by the client")
		}
	}
	fields := append(appScalar(3, 1), appScalar(4, 1)...)
	fields = append(fields, appScalar(5, 104)...)
	if _, err := g.execute("/MissionClear", fields); err == nil || g.wallet() != before {
		t.Fatal("expired collection mission paid a reward")
	}
}
