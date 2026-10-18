//go:build integration

package app

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/runtime/player"
)

// GameData 20261012190548 LocalText 7900026 promises a single reward per
// board first clear; 7900084 requires every stage win; 7900122 resets run items.
// IbStageEndResponse and RewardDBInfoBundle field numbers are from the current
// extracted Proto.Net classes. This exercises the actual account transaction,
// economy and HTTP handler chain, including a failed multi-request command.
func TestIBFirstClearAccountTransactionAndRecovery(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	d := factory.design.ib
	var seasons []uint64
	for id := range d.Seasons {
		seasons = append(seasons, id)
	}
	slices.Sort(seasons)
	season := seasons[0]
	var board gamedata.IBDungeon
	for _, id := range d.Seasons[season] {
		if candidate := d.Dungeons[id]; candidate.Unlock == 0 {
			board = candidate
			break
		}
	}
	if board.ID == 0 || board.Reward.Type != 70 {
		t.Fatal("selected design has no soul-board initial currency reward")
	}
	now := time.Now()
	found := false
	for n, content := range factory.options.calendars.RegularService.Contents {
		if content.ID == 9 {
			factory.options.calendars.RegularService.Contents[n].Current = schedule.Season{ID: season, StartMilliseconds: uint64(now.Add(-time.Hour).UnixMilli()), EndMilliseconds: uint64(now.Add(time.Hour).UnixMilli())}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("calendar has no IB content")
	}
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if e := g.owner.Close(); e != nil {
				t.Error(e)
			}
		}
	})
	balance := func() uint64 {
		t.Helper()
		tx, e := g.owner.repository.BeginCommand(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback() }()
		currencies, e := g.owner.assembly.eventEconomy.AdditionalCurrencies(command.Context{State: tx})
		if e != nil {
			t.Fatal(e)
		}
		return currencies[71]
	}
	baseline := balance()
	g.call("/IbMainInfo", nil)
	entered := g.call("/IbDungeonEnter", appScalar(2, board.ID))
	if len(appRows(entered, 2)) == 0 || len(appRows(entered, 3)) == 0 {
		t.Fatal("handler chain did not create initial board inventory and deck")
	}
	for n, stage := range board.Stages {
		request := append(appScalar(2, board.ID), appScalar(3, stage.ID)...)
		started := g.call("/IbStageStart", request)
		g.reopen()
		if resumed := g.call("/IbStageStart", request); !bytes.Equal(started, resumed) {
			t.Fatal("account reload changed battle process")
		}
		if n+1 < len(board.Stages) {
			g.call("/IbStageEnd", appScalar(2, 1))
			continue
		}
		failed := g.request("/IbStageEnd", appScalar(2, 1))
		failed.Requests = append(failed.Requests, player.Request{Path: "/IbDungeonEnter", Body: append(appScalar(1, 9000), appScalar(2, 999999)...)})
		g.execute(failed, true)
		g.reopen()
		if balance() != baseline {
			t.Fatal("failed batch committed first-clear currency")
		}
		main := g.call("/IbMainInfo", nil)
		play := appRows(main, 1)[0]
		if appValue(play, 7) != 1 || appValue(play, 2) != stage.ID {
			t.Fatal("failed batch lost the active final battle")
		}
		claim := g.request("/IbStageEnd", appScalar(2, 1))
		paid := g.execute(claim, false)
		if appValue(paid, 2) != 1 || appValue(paid, 3) != 1 || balance() != baseline+board.Reward.Count {
			t.Fatal("first clear did not atomically credit the real account currency")
		}
		g.reopen()
		if retry := g.execute(claim, false); !bytes.Equal(paid, retry) || balance() != baseline+board.Reward.Count {
			t.Fatal("durable claim retry paid again or changed response")
		}
	}
	if len(appRows(g.call("/IbItemInfo", nil), 1)) != 0 || len(appRows(g.call("/IbDeckInfo", nil), 1)) != 0 {
		t.Fatal("board completion retained run inventory")
	}
	g.call("/IbDungeonEnter", appScalar(2, board.ID))
	for _, stage := range board.Stages {
		g.call("/IbStageStart", append(appScalar(2, board.ID), appScalar(3, stage.ID)...))
		out := g.call("/IbStageEnd", appScalar(2, 1))
		if appValue(out, 3) != 0 {
			t.Fatal("rechallenge was marked first clear")
		}
	}
	g.reopen()
	if balance() != baseline+board.Reward.Count {
		t.Fatal("rechallenge minted additional first-clear currency")
	}
}
