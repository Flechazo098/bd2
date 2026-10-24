//go:build integration

package roguelike

import (
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"math"
	"testing"
)

type seasonBoard struct{ total uint64 }

func (b seasonBoard) Total(command.Context, uint64) (uint64, error) { return b.total, nil }
func (b seasonBoard) Snapshot(_ command.Context, _ uint64, mine RankEntry) ([]RankEntry, RankEntry, error) {
	mine.Rank = 2
	return []RankEntry{{Owner: 99, Score: 1, Rank: 1}, mine}, mine, nil
}

// Event semantics are read from RogueLikeInfo.ERogueLikeEventType and
// RogueLikeEventUI; upgrades, recipe matching, and payout are exercised through
// protocol requests against real current rules, with state persisted between them.
func TestEventShopRelicGrowthAndUnfinishedSeasonLifecycle(t *testing.T) {
	d := currentDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{AccountID: "branch-player", SessionID: "branch-login"}, State: store}
	economy := &testEconomy{}
	period := Period{Season: 1, Active: true}
	proof := battle.SettlementEvidence{}
	access := true
	tickets := uint64(0)
	configure := func() *Service {
		s, e := Open(stateio.EntrySnapshotStore{Domain: "battle", Bucket: "gameplay"}, d, Options{Owner: 4, Access: func(command.Context) error {
			if !access {
				return fail("locked")
			}
			return nil
		}, Economy: economy, Period: func(command.Context) (Period, error) { return period, nil }, Tickets: func(_ command.Context, _ string, c uint64) error { tickets += c; return nil }, Evidence: func(command.Context) battle.SettlementEvidence { return proof }, Ranking: seasonBoard{total: math.MaxInt64}})
		if e != nil {
			t.Fatal(e)
		}
		s.sample = func(uint64) uint64 { return 0 }
		return s
	}
	s := configure()
	access = false
	assertRejected(t, s, ctx, "Enter", req(1, 1))
	if tickets != 0 {
		t.Fatal("locked player consumed tickets")
	}
	access = true
	rpc(t, s, ctx, "Enter", req(2, 1))
	seq := uint64(3)
	for range 5 {
		v, _, e := s.load(ctx)
		if e != nil {
			t.Fatal(e)
		}
		rpc(t, s, ctx, "SelectReward", req(seq, 1, 0, v.Run.Choice[0]))
		seq++
	}
	v, _, _ := s.load(ctx)
	r := v.Run
	// A persisted server-selected event option grants gold. Find the semantic
	// GetGold option, rather than depending on a particular event ID or amount.
	var eventKey [2]uint64
	var option uint64
	for key, event := range d.Events.Rows {
		for i, id := range event.A(1) {
			if d.Effects.Row(0, id).V(1) == 2 && event.A(10)[i] == 100 {
				eventKey = key
				option = uint64(i + 1)
				break
			}
		}
		if option > 0 {
			break
		}
	}
	if option == 0 {
		t.Fatal("current rules contain no guaranteed gold event")
	}
	r.State = 5
	r.Room = 1
	r.EventGroup = eventKey[0]
	r.EventID = eventKey[1]
	beforeGold := r.Gold
	if e := s.save(ctx, v); e != nil {
		t.Fatal(e)
	}
	eventReq := req(seq, option)
	seq++
	eventOut := rpc(t, s, ctx, "EventSelect", eventReq)
	if !bytes.Equal(eventOut, rpc(t, s, ctx, "EventSelect", eventReq)) {
		t.Fatal("event retry rerolled outcome")
	}
	s = configure()
	v, _, _ = s.load(ctx)
	if v.Run.Gold <= beforeGold || v.Run.State != 0 {
		t.Fatal("event did not award local gold and complete the room")
	}
	// Shop stock persists, buying consumes the offer exactly once, and owned
	// costumes can be upgraded instead of disappearing from subsequent offers.
	r = v.Run
	r.Floor = 2
	r.Room = 1
	r.State = 4
	r.Gold = math.MaxInt32 / 2
	var shopRoom room
	for key, rule := range d.Rooms.Rows {
		if rule.V(10) == 6 {
			shopRoom = room{Number: 1, Group: key[0], ID: key[1]}
			break
		}
	}
	r.Floors[1].Rooms = []room{shopRoom}
	if e := s.newShop(v); e != nil {
		t.Fatal(e)
	}
	if e := s.save(ctx, v); e != nil {
		t.Fatal(e)
	}
	s = configure()
	v, _, _ = s.load(ctx)
	stock := v.Run.Shop[0]
	gold := v.Run.Gold
	buy := req(seq, stock.Type, stock.ID)
	seq++
	purchased := rpc(t, s, ctx, "ShopBuy", buy)
	if !bytes.Equal(purchased, rpc(t, s, ctx, "ShopBuy", buy)) {
		t.Fatal("shop retry changed receipt")
	}
	v, _, _ = s.load(ctx)
	if v.Run.Gold >= gold {
		t.Fatal("shop purchase did not consume run currency")
	}
	assertRejected(t, s, ctx, "ShopBuy", req(seq, stock.Type, stock.ID))
	seq++
	var upgrade costume
	for _, c := range v.Run.Costumes {
		if c.ID == stock.ID {
			upgrade = c
		}
	}
	if upgrade.Index == 0 {
		t.Fatal("shop costume not owned")
	}
	rpc(t, s, ctx, "CostumeUpgrade", req(seq, upgrade.Index, 1))
	seq++
	v, _, _ = s.load(ctx)
	found := false
	for _, c := range v.Run.Costumes {
		if c.Index == upgrade.Index && c.Level > upgrade.Level {
			found = true
		}
	}
	if !found {
		t.Fatal("shop upgrade not persisted")
	}
	// Use an authored recipe as the scenario setup, then exercise the public
	// atomic consumption and reject reusing the consumed instances.
	recipe := d.Mix.Row(0, d.Mix.Groups[0][0])
	left, e := s.addRelic(v, recipe.V(2))
	if e != nil {
		t.Fatal(e)
	}
	right, e := s.addRelic(v, recipe.V(3))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.save(ctx, v); e != nil {
		t.Fatal(e)
	}
	mix := wire.AppendVarint(wire.AppendVarint(req(seq), 2, left.Index), 2, right.Index)
	seq++
	rpc(t, s, ctx, "RelicMix", mix)
	s = configure()
	v, _, _ = s.load(ctx)
	haveResult := false
	for _, item := range v.Run.Relics {
		if item.Index == left.Index || item.Index == right.Index {
			t.Fatal("recipe material survived mix")
		}
		if item.ID == recipe.V(4) {
			haveResult = true
		}
	}
	if !haveResult {
		t.Fatal("recipe output absent")
	}
	changedMix, _, _ := wire.ReplaceVarint(mix, 1, seq)
	seq++
	assertRejected(t, s, ctx, "RelicMix", changedMix)
	// Permanent growth is purchased between runs and carried into the next
	// independent roster. The value is a relationship, not a fixed table amount.
	rpc(t, s, ctx, "GiveUp", req(seq))
	seq++
	v, _, _ = s.load(ctx)
	growth := d.Growth.Row(1, 1)
	v.Obsidian = growth.V(8)
	if e = s.save(ctx, v); e != nil {
		t.Fatal(e)
	}
	request := wire.AppendBytes(req(seq), 2, wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1))
	seq++
	rpc(t, s, ctx, "Growth", request)
	s = configure()
	rpc(t, s, ctx, "Enter", req(seq, 1))
	seq++
	v, _, _ = s.load(ctx)
	id := v.Run.Choice[0]
	rpc(t, s, ctx, "SelectReward", req(seq, 1, 0, id))
	seq++
	v, _, _ = s.load(ctx)
	if v.Run.Characters[0].MaxHP <= d.Costumes[id].Health {
		t.Fatal("purchased HP growth did not affect new run")
	}
	for remaining := v.Run.Remaining; remaining > 0; remaining-- {
		rpc(t, s, ctx, "SelectReward", req(seq, 1, 0, v.Run.Choice[0]))
		seq++
		v, _, _ = s.load(ctx)
	}
	character := v.Run.Characters[0]
	var outfit costume
	for _, c := range v.Run.Costumes {
		if c.Index == character.Costume {
			outfit = c
		}
	}
	deck := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, character.Index), 2, 0), 3, 1)
	rpc(t, s, ctx, "DeckSave", wire.AppendBytes(req(seq), 2, deck))
	seq++
	rpc(t, s, ctx, "RoomEnter", req(seq, 1))
	seq++
	v, _, _ = s.load(ctx)
	battleDeck, mode := s.battleRule(v.Run)
	if _, e = s.EnterBattle(ctx, req(seq, 0, 0, battleDeck, mode), "grown-battle"); e != nil {
		t.Fatal(e)
	}
	seq++
	proof = battle.SettlementEvidence{Receipt: "grown-battle", Mode: mode, Deck: battleDeck}
	blue := wire.AppendVarint(nil, 1, 1)
	for _, f := range []struct {
		n int
		v uint64
	}{{2, character.Index}, {3, character.ID}, {4, character.HP}, {5, d.Default.V(7)}, {6, outfit.Index}, {7, outfit.ID}, {8, outfit.Level}} {
		blue = wire.AppendVarint(blue, f.n, f.v)
	}
	start := wire.AppendBytes(wire.AppendBytes(req(seq, battleDeck), 3, deck), 5, blue)
	seq++
	for i, enemy := range d.Enemies[battleDeck] {
		red := wire.AppendVarint(nil, 1, uint64(i+2))
		red = wire.AppendVarint(red, 3, enemy.ID)
		red = wire.AppendVarint(red, 4, 1)
		red = wire.AppendVarint(red, 5, enemy.Level)
		red = wire.AppendVarint(red, 9, enemy.Position)
		start = wire.AppendBytes(start, 4, red)
	}
	if e = s.ValidateBattleStart(ctx, start); e != nil {
		t.Fatalf("legitimate HP growth battle refused: %v", e)
	}
	proof.Round = 1
	proof.InitialBlue = [][]byte{blue}
	if _, e = s.CompleteBattle(ctx, wire.AppendBytes(req(seq, 1), 3, charWire(character, d.Default.V(7))), "grown-battle"); e != nil {
		t.Fatal(e)
	}
	seq++
	rpc(t, s, ctx, "GiveUp", req(seq))
	seq++
	period.Season = 2
	s = configure()
	beforeRewards := economy.rewards
	claim := req(seq)
	seq++
	assertRejected(t, s, ctx, "SeasonReward", claim)
	if economy.rewards != beforeRewards {
		t.Fatal("unfinished runs received a completed-season reward")
	}
}
