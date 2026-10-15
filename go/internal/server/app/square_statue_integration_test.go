//go:build integration

package app

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/world"
	"bd2server/internal/server/storage/outbox"
)

func newSquareGameplay(t *testing.T, factory *PlayerFactory, account string) *puzzleGameplay {
	t.Helper()
	owner, err := factory.open(account)
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: account, owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return g
}
func squareBalance(t *testing.T, g *puzzleGameplay, kind, id uint64) uint64 {
	t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{State: tx}
	if kind == 4 {
		return g.owner.assembly.wallet.Snapshot(ctx).Gold
	}
	if field, ok := events.AdditionalCurrencyFields[kind]; ok {
		balances, err := g.owner.assembly.eventEconomy.AdditionalCurrencies(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return balances[field]
	}
	var count uint64
	for _, item := range g.owner.assembly.ownedItems.All(ctx) {
		if item.Type == kind && item.ID == id {
			count += item.Count
		}
	}
	return count
}

// LocalText1500190 allows one probabilistic goddess reward per day;
// SquarePacket applies the returned bundle and clears obtainability only after success.
func TestSquareRewardCommitsNativeAssetsOnceAndRollsBackWithBatch(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	g := newSquareGameplay(t, factory, accounts[0])
	before := map[[2]uint64]uint64{}
	for _, r := range factory.design.eventActionsDesign.Tables["SquareRewardTable"] {
		k := [2]uint64{r.V(6), r.V(5)}
		before[k] = squareBalance(t, g, k[0], k[1])
	}
	if appValue(g.call("/SquareRewardInfo", nil), 1) != 1 {
		t.Fatal("fresh account lacks daily goddess reward")
	}
	failed := g.request("/SquareReward", nil)
	bad := g.request("/StatueHit", appMessage(2, append(appScalar(1, 999999), appScalar(2, 1)...)))
	failed.Requests = append(failed.Requests, bad.Requests...)
	g.execute(failed, true)
	g.reopen()
	for key, count := range before {
		if squareBalance(t, g, key[0], key[1]) != count {
			t.Fatal("failed batch credited a goddess reward")
		}
	}
	if appValue(g.call("/SquareRewardInfo", nil), 1) != 1 {
		t.Fatal("rollback consumed goddess claim")
	}
	claim := g.request("/SquareReward", nil)
	body := g.execute(claim, false)
	chosen := appValue(body, 1)
	bundles := appRows(body, 2)
	if len(bundles) != 1 {
		t.Fatal("claim lacks client reward bundle")
	}
	items := appRows(bundles[0], 1)
	if len(items) != 1 {
		t.Fatal("goddess reward should grant one selected entry")
	}
	kind, id, count := appValue(items[0], 3), appValue(items[0], 2), appValue(items[0], 4)
	matched := false
	for _, r := range factory.design.eventActionsDesign.Tables["SquareRewardTable"] {
		if r.V(1) == chosen {
			matched = kind == r.V(6) && id == r.V(5) && count >= r.V(4) && count <= r.V(3)
		}
	}
	if !matched || squareBalance(t, g, kind, id) != before[[2]uint64{kind, id}]+count {
		t.Fatal("selected authored reward did not reach native player assets")
	}
	g.reopen()
	if replay := g.execute(claim, false); !bytes.Equal(replay, body) {
		t.Fatal("reopen changed claim replay")
	}
	if squareBalance(t, g, kind, id) != before[[2]uint64{kind, id}]+count || appValue(g.call("/SquareRewardInfo", nil), 1) != 0 {
		t.Fatal("retry duplicated reward or claim did not persist")
	}
	g.execute(g.request("/SquareReward", nil), true)
	if squareBalance(t, g, kind, id) != before[[2]uint64{kind, id}]+count {
		t.Fatal("new sequence bypassed daily claim")
	}
	other := newSquareGameplay(t, factory, accounts[1])
	if appValue(other.call("/SquareRewardInfo", nil), 1) != 1 {
		t.Fatal("one player's goddess claim consumed another's reward")
	}
}

// LocalText1500361 pools every player's increments; the client sends Count
// deltas while PersonalCount is presentation data, never an authoritative total.
func TestStatueHitsAggregateCommittedPlayersWithoutDuplicateDelivery(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	shared, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := shared.store.Close(); err != nil {
			t.Error(err)
		}
	})
	factory.shared, factory.pvpSource = shared, shared.pvp
	a := newSquareGameplay(t, factory, accounts[0])
	b := newSquareGameplay(t, factory, accounts[1])
	id := factory.design.eventActionsDesign.Tables["FieldStatueEventTable"][0].V(3)
	hit := func(g *puzzleGameplay, count uint64) {
		g.call("/StatueHit", appMessage(2, append(append(appScalar(1, id), appScalar(2, count)...), appScalar(3, 999999)...)))
	}
	hit(a, 4)
	hit(a, 7)
	hit(b, 5)
	info := func(g *puzzleGameplay) (uint64, uint64) {
		for _, row := range appRows(g.call("/StatueHitInfo", nil), 1) {
			if appValue(row, 1) == id {
				return appValue(row, 2), appValue(row, 3)
			}
		}
		t.Fatal("statue omitted from query")
		return 0, 0
	}
	if total, personal := info(a); total != 11 || personal != 11 {
		t.Fatal("unpublished own increments disappeared")
	}
	for _, g := range []*puzzleGameplay{a, b} {
		tx, e := g.owner.repository.BeginCommand(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		pending, e := outbox.Read(tx, 128)
		_ = tx.Rollback()
		if e != nil {
			t.Fatal(e)
		}
		for _, event := range slices.Backward(pending) {
			if event.Envelope.Event.Topic != "square.statue_hits" {
				continue
			}
			for range 2 {
				if e = shared.Consume(context.Background(), event); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	a.reopen()
	b.reopen()
	if total, personal := info(a); total != 16 || personal != 11 {
		t.Fatalf("first player's cumulative counts=%d/%d", total, personal)
	}
	if total, personal := info(b); total != 16 || personal != 5 {
		t.Fatalf("second player's cumulative counts=%d/%d", total, personal)
	}
}

func TestRankStatueRewardPersistsClaimAndDoesNotRegrantOnPackEntry(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	g := newSquareGameplay(t, factory, accounts[0])
	view := func(command.Context) ([]world.RankStatue, error) {
		return []world.RankStatue{{ID: 3, Season: 80, Groups: [][]byte{appScalar(1, 3)}}}, nil
	}
	g.owner.assembly.worldService.AttachRankStatues(view)
	g.call("/PackInGameInfo", appScalar(2, 1))
	before := squareBalance(t, g, 4, 0)
	claim := g.request("/StatueObjectReward", appScalar(2, 3))
	body := g.execute(claim, false)
	if len(appRows(body, 1)) != 1 || squareBalance(t, g, 4, 0) != before+20000 {
		t.Fatal("pack1 fiend statue did not credit its authored20000gold")
	}
	g.reopen()
	g.owner.assembly.worldService.AttachRankStatues(view)
	pack := g.call("/PackInGameInfo", appScalar(2, 1))
	claimed := false
	for _, id := range puzzleNumbers(t, pack, 15) {
		if id == 3 {
			claimed = true
		}
	}
	if !claimed {
		t.Fatal("pack entry did not restore obtained statue marker")
	}
	g.execute(claim, false)
	g.call("/StatueObjectReward", appScalar(2, 3))
	if squareBalance(t, g, 4, 0) != before+20000 {
		t.Fatal("statue claim retried or revisited pack granted reward twice")
	}
}
