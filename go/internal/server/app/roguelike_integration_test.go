//go:build integration

package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"encoding/binary"
	"testing"
)

// The packet numbers, 1-based sequence, and independent run roster follow
// Proto.Net.PacketCodeTypeProto, RogueLikePacket.CreateDeckList and BattlePacket.
func TestRogueLikeNativeBattleReopenAndTicketConsumption(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	a := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, a.owner) })
	seq := uint64(1)
	call := func(path string, body []byte, code int) []byte {
		t.Helper()
		reply, e := a.execute(path, body)
		if e != nil {
			t.Fatalf("%s: %v", path, e)
		}
		if len(reply.Responses) != 1 || reply.Responses[0].PacketCode != code {
			t.Fatalf("%s packet mismatch: %+v", path, reply.Responses)
		}
		if e = wire.Walk(reply.Responses[0].Body, func(wire.Field) error { return nil }); e != nil {
			t.Fatal(e)
		}
		return reply.Responses[0].Body
	}
	var before uint64
	a.run(t, true, func(ctx command.Context, p *playerAssembly) {
		var e error
		before, e = p.eventEconomy.CurrencyBalance(ctx, 35)
		if e != nil {
			t.Fatal(e)
		}
		rule := p.design.roguelike.Open
		grants := []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}
		pack := p.design.roguelike.PackOpen
		if pack.TicketID != 0 {
			grants = append(grants, gamedata.BattleReward{Type: 19, ID: pack.TicketID, Count: 1})
		}
		if _, e = p.ownedItems.GrantOnce(ctx, "roguelike-unlock-test", grants); e != nil {
			t.Fatal(e)
		}
	})
	a.reopen(t)
	call("/PackInGameInfo", trapRequest(seq, factory.design.roguelike.Pack), 5)
	seq++
	call("/EvilCastleRogueLikeInfo", appScalar(1, seq), 279)
	seq++
	entered := call("/EvilCastleRogueLikeEnter", trapRequest(seq, 1), 280)
	seq++
	draftUnique := map[uint64]bool{}
	for i := range 5 {
		choice := appRows(entered, 2)
		if len(choice) == 0 {
			t.Fatal("draft choice absent")
		}
		ids := []uint64{}
		if e := wire.Walk(choice[0], func(f wire.Field) error {
			if f.Number == 2 && f.Type == 0 {
				v, k := binary.Uvarint(f.Value)
				if k <= 0 {
					return wire.ErrMalformed
				}
				ids = append(ids, v)
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		if len(ids) == 0 {
			t.Fatal("draft offered no costumes")
		}
		b := trapRequest(seq, 1)
		seq++
		selected := ids[0]
		if i == 0 {
			counts := map[uint64]int{}
			for _, c := range factory.design.roguelike.Costumes {
				counts[c.Unique]++
			}
			for _, id := range ids {
				if counts[factory.design.roguelike.Costumes[id].Unique] == 1 {
					selected = id
					break
				}
			}
		}
		for _, id := range ids {
			if i == 0 {
				break
			}
			unique := factory.design.roguelike.Costumes[id].Unique
			if !draftUnique[unique] {
				selected = id
				draftUnique[unique] = true
				break
			}
		}
		b = wire.AppendVarint(b, 4, selected)
		entered = call("/EvilCastleRogueLikeSelectReward", b, 283)
		a.reopen(t)
	}
	info := call("/EvilCastleRogueLikeInfo", appScalar(1, seq), 279)
	seq++
	characters, costumes := appRows(info, 5), appRows(info, 6)
	if len(costumes) != 5 || len(characters) == 0 {
		t.Fatal("draft roster was not persisted")
	}
	char := characters[0]
	index := appValue(char, 1)
	var costume []byte
	for _, c := range costumes {
		if appValue(c, 1) == appValue(char, 7) {
			costume = c
		}
	}
	if costume == nil {
		t.Fatal("draft character lost costume ownership")
	}
	deck := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, index), 2, 0), 3, 1)
	save := wire.AppendBytes(appScalar(1, seq), 2, deck)
	seq++
	call("/EvilCastleRogueLikeDeckSave", save, 285)
	floors := appRows(info, 3)
	room := appRows(floors[0], 2)[0]
	group, id := appValue(room, 2), appValue(room, 3)
	definition := factory.design.roguelike.Rooms.Row(group, id)
	battleDeck := definition.V(1)
	call("/EvilCastleRogueLikeRoomEnter", trapRequest(seq, appValue(room, 1)), 282)
	seq++
	enter := wire.AppendVarint(wire.AppendVarint(appScalar(1, seq), 4, battleDeck), 5, 21)
	seq++
	call("/BattleEnter", enter, 52)
	start := wire.AppendBytes(trapRequest(seq, battleDeck), 3, deck)
	seq++
	blue := wire.AppendVarint(nil, 1, 1)
	for _, f := range []struct {
		to    int
		value uint64
	}{{2, index}, {3, appValue(char, 2)}, {4, appValue(char, 3)}, {5, appValue(char, 4)}, {6, appValue(costume, 1)}, {7, appValue(costume, 2)}, {8, appValue(costume, 3)}} {
		blue = wire.AppendVarint(blue, f.to, f.value)
	}
	start = wire.AppendBytes(start, 5, blue)
	for i, enemy := range factory.design.roguelike.Enemies[battleDeck] {
		red := wire.AppendVarint(nil, 1, uint64(i+2))
		red = wire.AppendVarint(red, 3, enemy.ID)
		red = wire.AppendVarint(red, 4, 1)
		red = wire.AppendVarint(red, 5, enemy.Level)
		red = wire.AppendVarint(red, 9, enemy.Position)
		start = wire.AppendBytes(start, 4, red)
	}
	forged, _, e := wire.ReplaceVarint(blue, 7, appValue(costume, 2)+1)
	if e != nil {
		t.Fatal(e)
	}
	var bad []byte
	if e = wire.Walk(start, func(f wire.Field) error {
		switch f.Number {
		case 1:
			bad = wire.AppendVarint(bad, 1, seq)
		case 5:
			bad = wire.AppendBytes(bad, 5, forged)
		default:
			bad = append(bad, start[f.Start:f.End]...)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	seq++
	a.run(t, false, func(ctx command.Context, p *playerAssembly) {
		if _, _, _, e := p.battleService.Handle(ctx, "/BattleStart", bad); e == nil {
			t.Fatal("unowned battle costume bypassed the event start hook")
		}
	})
	call("/BattleStart", start, 14)
	incoming := characters[1]
	change := wire.AppendVarint(trapRequest(seq, appValue(incoming, 1)), 3, index)
	seq++
	call("/EvilCastleRogueLikeBattleCharChange", change, 317)

	a.reopen(t)
	end := wire.AppendBytes(trapRequest(seq, 1), 3, char)
	end = wire.AppendBytes(end, 3, incoming)
	seq++
	result := call("/BattleEnd", end, 15)
	state := appRows(result, 17)
	if len(state) != 1 || appValue(state[0], 3) != 1 {
		t.Fatal("normal battle settlement did not produce a run reward choice")
	}
	if !bytes.Equal(result, call("/BattleEnd", end, 15)) {
		t.Fatal("normal battle replay changed settlement")
	}
	a.reopen(t)
	resumed := call("/EvilCastleRogueLikeInfo", appScalar(1, seq), 279)
	seq++
	if appValue(appRows(resumed, 1)[0], 3) != 1 || len(appRows(resumed, 8)) != 1 {
		t.Fatal("reopening lost pending battle rewards")
	}
	a.run(t, false, func(ctx command.Context, p *playerAssembly) {
		after, e := p.eventEconomy.CurrencyBalance(ctx, 35)
		if e != nil {
			t.Fatal(e)
		}
		if before == 0 || after >= before {
			t.Fatal("entry did not consume real account tickets")
		}
	})
	choice := appRows(resumed, 8)[0]
	selected := wire.AppendVarint(trapRequest(seq, 1), 4, appValue(choice, 2))
	seq++
	call("/EvilCastleRogueLikeSelectReward", selected, 283)
	a.reopen(t)
	resumed = call("/EvilCastleRogueLikeInfo", appScalar(1, seq), 279)
	if appValue(appRows(resumed, 1)[0], 1) <= 1 {
		t.Fatalf("reward claim did not advance the resumed run: %x", resumed)
	}
}
