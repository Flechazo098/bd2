//go:build integration

package app

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"slices"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
)

// Current 20261012190548 GameData binds Greed floor 1 to pack3003 deck10001.
// Its win/no-death/8-turn challenges award material1000 x1, gold30000 and
// bonus hunting AP5. These expectations come from the read-only GameData query,
// independently of the castle loader and the runtime reward response.
func TestCastleTowerBattleRewardsAndChallengesSurviveAccountReopen(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	a := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	b := &trapPlayerScenario{factory: factory, account: accounts[1], owner: openNicknamePlayer(t, factory, accounts[1])}
	t.Cleanup(func() { closeNicknamePlayer(t, a.owner); closeNicknamePlayer(t, b.owner) })
	var blue, deck, character []byte
	a.run(t, true, func(ctx command.Context, p *playerAssembly) {
		// ContentOpenTable group1/3003 requires the actual Evil Castle ticket13003.
		if _, e := p.ownedItems.GrantOnce(ctx, "castle-entry-ticket", []gamedata.BattleReward{{Type: 19, ID: 13003, Count: 1}}); e != nil {
			t.Fatal(e)
		}
		for _, c := range p.worldService.CharacterService().RawAll() {
			if roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) {
				continue
			}
			costume, owned := p.collection.CostumeByIndex(c.UseCostume)
			if !owned || costume.UseChar != c.InvenIndex {
				continue
			}
			hp, e := p.worldService.CharacterService().MaxHealth(ctx, c.InvenIndex)
			if e != nil {
				t.Fatal(e)
			}
			blue = appScalar(1, 1)
			blue = append(blue, appScalar(2, c.InvenIndex)...)
			blue = append(blue, appScalar(3, c.ID)...)
			blue = append(blue, appScalar(4, hp)...)
			blue = append(blue, appScalar(6, costume.InvenIndex)...)
			blue = append(blue, appScalar(7, costume.ID)...)
			deck = appScalar(1, c.InvenIndex)
			deck = append(deck, appScalar(2, 0)...)
			deck = append(deck, appScalar(3, 1)...)
			character = roster.CharacterWire(c)
			character, _, e = wire.ReplaceVarint(character, 3, hp)
			if e != nil {
				t.Fatal(e)
			}
			break
		}
		if len(blue) == 0 {
			t.Fatal("starter player has no owned battle participant")
		}
	})
	a.reopen(t)
	a.protocol(t, "/PackInGameInfo", trapRequest(1, 3003))
	beforeGold := a.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	beforeMaterial := appItemTotals(a.protocol(t, "/ItemInfo", appScalar(1, 2)))[[2]uint64{8, 1000}]
	bGold := b.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	bMaterial := appItemTotals(b.protocol(t, "/ItemInfo", appScalar(1, 1)))[[2]uint64{8, 1000}]
	bonusAP := func(s *trapPlayerScenario) uint64 {
		t.Helper()
		var bonus uint64
		s.run(t, false, func(ctx command.Context, p *playerAssembly) {
			var e error
			_, bonus, e = p.huntingService.HuntingAP(ctx)
			if e != nil {
				t.Fatal(e)
			}
		})
		return bonus
	}
	beforeAP, otherAP := bonusAP(a), bonusAP(b)
	enter := appScalar(1, 3)
	enter = append(enter, appScalar(4, 10001)...)
	enter = append(enter, appScalar(5, 16)...)
	enter = append(enter, appScalar(9, 1)...)
	a.protocol(t, "/BattleEnter", enter)
	common := appScalar(1, 16)
	common = append(common, appScalar(2, 10001)...)
	checkpoint := appMessage(1, common)
	checkpoint = append(checkpoint, appMessage(8, []byte(base64.StdEncoding.EncodeToString(appMessage(3, blue))))...)
	start := trapRequest(4, 10001)
	start = wire.AppendBytes(start, 3, deck)
	start = wire.AppendBytes(start, 5, blue)
	start = wire.AppendBytes(start, 100, checkpoint)
	a.protocol(t, "/BattleStart", start)
	// Reassembly must retain the normal battle's owned party and round evidence.
	a.reopen(t)
	end := trapRequest(5, 1)
	end = wire.AppendBytes(end, 3, character)
	for _, index := range []uint64{0, 1, 2} {
		end = wire.AppendVarint(end, 5, index)
	}
	settled := a.protocol(t, "/BattleEnd", end)
	assertAssets := func() {
		t.Helper()
		if a.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold+30000 || appItemTotals(a.protocol(t, "/ItemInfo", appScalar(1, uint64(a.commands+100))))[[2]uint64{8, 1000}] != beforeMaterial+1 || bonusAP(a) != beforeAP+5 {
			t.Fatal("tower challenges did not credit their authored gold, material and hunting AP exactly once")
		}
	}
	assertAssets()
	assertTower := func(s *trapPlayerScenario, want bool) {
		t.Helper()
		rows := appRows(s.protocol(t, "/EvilCastleTowerInfo", appScalar(1, uint64(s.commands+100))), 1)
		if !want {
			if len(rows) != 0 {
				t.Fatal("unplayed account acquired another player's tower progress")
			}
			return
		}
		if len(rows) != 1 || appValue(rows[0], 1) != 1 || appValue(rows[0], 3) != 16 {
			t.Fatal("restored tower floor/mode differs from the played encounter")
		}
		var indexes []uint64
		if e := wire.Walk(rows[0], func(f wire.Field) error {
			if f.Number == 2 {
				for raw := f.Value; len(raw) > 0; {
					value, count := binary.Uvarint(raw)
					if count <= 0 {
						return wire.ErrMalformed
					}
					indexes = append(indexes, value)
					raw = raw[count:]
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		if !slices.Equal(indexes, []uint64{0, 1, 2}) {
			t.Fatalf("completed tower challenges were lost or fabricated: %v", indexes)
		}
	}
	assertTower(a, true)
	assertTower(b, false)
	if replay := a.protocol(t, "/BattleEnd", end); !bytes.Equal(replay, settled) {
		t.Fatal("battle settlement replay changed its reply")
	}
	assertAssets()
	a.reopen(t)
	b.reopen(t)
	assertTower(a, true)
	assertTower(b, false)
	assertAssets()
	if replay := a.protocol(t, "/BattleEnd", end); !bytes.Equal(replay, settled) {
		t.Fatal("reopened battle receipt changed its reply")
	}
	assertAssets()
	if b.owner.assembly.wallet.Snapshot(command.Context{}).Gold != bGold || appItemTotals(b.protocol(t, "/ItemInfo", appScalar(1, uint64(b.commands+100))))[[2]uint64{8, 1000}] != bMaterial || bonusAP(b) != otherAP {
		t.Fatal("tower rewards crossed account boundaries")
	}
}
