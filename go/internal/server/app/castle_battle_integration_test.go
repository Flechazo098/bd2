//go:build integration

package app

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
)

// GameData 20261021182514 binds Greed floor 1 to pack3003 deck10001.
// Its win/no-death/8-turn challenges award material1000 x1, gold30000 and
// bonus hunting AP5. These expectations come from the read-only GameData query,
// independently of the castle loader and the runtime reward response.
// Evil Castle field battles send only the costume design ID in the initial
// blue team; the costume inventory index is not part of the request. Settlement
// must still validate the party and record the cleared tower floor.
func TestCastleFieldBattleSettlesWithoutCostumeInventoryIndex(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	a := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, a.owner) })
	var blue, char []byte
	a.run(t, true, func(ctx command.Context, p *playerAssembly) {
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
			blue = append(blue, appScalar(7, costume.ID)...)
			char = appScalar(1, c.InvenIndex)
			char = append(char, appScalar(2, c.ID)...)
			char = append(char, appScalar(3, hp)...)
			break
		}
		if len(blue) == 0 {
			t.Fatal("starter player has no owned battle participant")
		}
	})
	a.reopen(t)
	a.protocol(t, "/PackInGameInfo", trapRequest(1, 3003))
	enter := appScalar(1, 3)
	enter = append(enter, appScalar(4, 10001)...)
	enter = append(enter, appScalar(5, 16)...)
	enter = append(enter, appScalar(9, 1)...)
	a.protocol(t, "/BattleEnter", enter)
	start := trapRequest(4, 10001)
	start = wire.AppendBytes(start, 5, blue)
	a.protocol(t, "/BattleStart", start)
	end := trapRequest(5, 1)
	end = wire.AppendBytes(end, 3, char)
	a.protocol(t, "/BattleEnd", end)
	if rows := appRows(a.protocol(t, "/EvilCastleTowerInfo", appScalar(1, uint64(a.commands+100))), 1); len(rows) != 1 || appValue(rows[0], 1) != 1 || appValue(rows[0], 3) != 16 {
		t.Fatal("settled field battle did not record its tower floor")
	}
}

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

// Field battle costume selection is independent of the equipped costume.
func TestCastleFieldBattleValidatesSelectedCostumeOwnershipAndGrowthFamily(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	a := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, a.owner) })
	var character, other roster.Character
	var equippedIndex uint64
	a.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.ownedItems.GrantOnce(ctx, "castle-entry-ticket", []gamedata.BattleReward{{Type: 19, ID: 13003, Count: 1}}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.collection.GrantCostumes(ctx, "castle-alternate-costumes", []uint64{3801, 3803, 501, 506}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.collection.Characters() {
			if c.ID == 380 {
				character = c
				character.ID = 384
				character.Level = 100
				if err := p.collection.UpdateCharacter(ctx, c.ID, character); err != nil {
					t.Fatal(err)
				}
			}
			if c.ID == 50 {
				other = c
				other.ID = 54
				other.Level = 100
				if err := p.collection.UpdateCharacter(ctx, c.ID, other); err != nil {
					t.Fatal(err)
				}
			}
		}
		if character.InvenIndex == 0 || other.InvenIndex == 0 {
			t.Fatal("missing owned character 380 for growth fixture")
		}
		equipped, ok := p.collection.CostumeByIndex(character.UseCostume)
		if !ok || equipped.ID != 3801 {
			t.Fatal("fixture must equip 3801 rather than selected 3803")
		}
		equippedIndex = equipped.InvenIndex
		if _, ok := p.collection.CostumeByID(3802); ok {
			t.Fatal("unowned-costume fixture already owned")
		}
	})
	a.reopen(t)
	a.protocol(t, "/PackInGameInfo", trapRequest(1, 3003))
	a.run(t, false, func(ctx command.Context, p *playerAssembly) {
		hp, err := p.worldService.CharacterService().MaxHealth(ctx, character.InvenIndex)
		if err != nil {
			t.Fatal(err)
		}
		character.HP = hp
		hp, err = p.worldService.CharacterService().MaxHealth(ctx, other.InvenIndex)
		if err != nil {
			t.Fatal(err)
		}
		other.HP = hp
		if _, inPool := p.design.regularGacha.Character(506); inPool {
			t.Fatal("fixture 506 must exercise a costume outside the current gacha pool")
		}
	})
	for i, tc := range []struct {
		name           string
		costume, index uint64
		errorText      string
	}{
		{"unowned", 3802, 0, "costume unavailable"},
		{"different character", 506, 0, "costume belongs to a different character"},
		{"explicit index mismatch", 3803, equippedIndex, "costume unavailable"},
		{"owned alternate costume", 3803, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seq := uint64(50 + i*10)
			a.protocol(t, "/PackInGameInfo", trapRequest(seq-1, 3003))
			enter := append(appScalar(1, seq), appScalar(4, 10001)...)
			enter = append(enter, appScalar(5, 16)...)
			enter = append(enter, appScalar(9, 1)...)
			a.protocol(t, "/BattleEnter", enter)
			blue := appScalar(1, 1)
			for _, f := range [][2]uint64{{2, character.InvenIndex}, {3, character.ID}, {4, character.HP}, {6, tc.index}, {7, tc.costume}} {
				blue = append(blue, appScalar(int(f[0]), f[1])...)
			}
			start := wire.AppendBytes(trapRequest(seq+1, 10001), 5, blue)
			if tc.errorText == "" {
				selected := appScalar(1, 2)
				for _, f := range [][2]uint64{{2, other.InvenIndex}, {3, other.ID}, {4, other.HP}, {7, 506}} {
					selected = append(selected, appScalar(int(f[0]), f[1])...)
				}
				start = wire.AppendBytes(start, 5, selected)
			}
			a.protocol(t, "/BattleStart", start)
			final := append(appScalar(1, character.InvenIndex), appScalar(2, character.ID)...)
			final = append(final, appScalar(3, character.HP)...)
			end := wire.AppendBytes(trapRequest(seq+2, 1), 3, final)
			if tc.errorText != "" {
				gold := a.owner.assembly.wallet.Snapshot(command.Context{}).Gold
				if _, err := a.execute("/BattleEnd", end); err == nil || !strings.Contains(err.Error(), tc.errorText) {
					t.Fatalf("invalid costume settlement: %v", err)
				}
				a.reopen(t)
				if got := a.owner.assembly.wallet.Snapshot(command.Context{}).Gold; got != gold {
					t.Fatal("rejected costume settlement granted gold")
				}
				if len(appRows(a.protocol(t, "/EvilCastleTowerInfo", appScalar(1, seq+3)), 1)) != 0 {
					t.Fatal("rejected costume cleared tower")
				}
				a.protocol(t, "/BattleExit", appScalar(1, seq+4))
				return
			}
			finalOther := append(appScalar(1, other.InvenIndex), appScalar(2, other.ID)...)
			finalOther = append(finalOther, appScalar(3, other.HP)...)
			end = wire.AppendBytes(end, 3, finalOther)
			settled := a.protocol(t, "/BattleEnd", end)
			if !bytes.Equal(settled, a.protocol(t, "/BattleEnd", end)) {
				t.Fatal("settlement retry changed response")
			}
			a.reopen(t)
			rows := appRows(a.protocol(t, "/EvilCastleTowerInfo", appScalar(1, seq+3)), 1)
			if len(rows) != 1 || appValue(rows[0], 1) != 1 || appValue(rows[0], 3) != 16 {
				t.Fatal("alternate costume victory was not persisted")
			}
		})
	}
}

func TestPrideBossResumeCompletesFloorBeforeRewardsBecomeAvailable(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	a := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, a.owner) })
	var blue, final []byte
	a.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.ownedItems.GrantOnce(ctx, "pride-ticket", []gamedata.BattleReward{{Type: 19, ID: 13003, Count: 1}}); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.worldService.CharacterService().RawAll() {
			if roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) {
				continue
			}
			costume, ok := p.collection.CostumeByIndex(c.UseCostume)
			if !ok {
				continue
			}
			hp, err := p.worldService.CharacterService().MaxHealth(ctx, c.InvenIndex)
			if err != nil {
				t.Fatal(err)
			}
			blue = appScalar(1, 1)
			for _, f := range [][2]uint64{{2, c.InvenIndex}, {3, c.ID}, {4, hp}, {7, costume.ID}} {
				blue = append(blue, appScalar(int(f[0]), f[1])...)
			}
			final = append(appScalar(1, c.InvenIndex), appScalar(2, c.ID)...)
			final = append(final, appScalar(3, hp)...)
			break
		}
		if len(blue) == 0 {
			t.Fatal("missing owned participant")
		}
	})
	a.reopen(t)
	seq := uint64(1)
	call := func(path string, body []byte) []byte {
		t.Helper()
		seq++
		return a.protocol(t, path, append(appScalar(1, seq), body...))
	}
	call("/PackInGameInfo", appScalar(2, 3003))
	call("/EvilCastleEnter", append(appScalar(2, 3003), appScalar(3, 1)...))
	var score uint64
	// Current GameData floor 1: ordinary monsters 201..204, boss 210.
	for _, monster := range []uint64{201, 202, 203, 204, 210} {
		call("/PackInGameInfo", appScalar(2, 3003))
		deck := factory.design.castle.Floors[1].Monsters[monster]
		if deck == 0 {
			t.Fatalf("current floor no longer contains monster %d", monster)
		}
		enter := append(appScalar(3, monster), appScalar(4, deck)...)
		enter = append(enter, appScalar(5, 7)...)
		call("/BattleEnter", enter)
		common := append(appScalar(1, 7), appScalar(2, deck)...)
		checkpoint := appMessage(1, common)
		checkpoint = append(checkpoint, appMessage(8, []byte(base64.StdEncoding.EncodeToString(appMessage(3, blue))))...)
		checkpoint = append(checkpoint, appScalar(101, 1)...)
		start := wire.AppendBytes(appScalar(2, deck), 5, blue)
		start = wire.AppendBytes(start, 100, checkpoint)
		call("/BattleStart", start)
		if monster == 210 {
			a.reopen(t)
			resumed := call("/BattleResume", nil)
			monsters := appRows(resumed, 6)
			if appValue(resumed, 19) != 1 || appValue(resumed, 7) != score || len(monsters) != 1 || appValue(monsters[0], 1) != 210 || appValue(monsters[0], 2) != deck {
				t.Fatal("boss resume lost floor, score or live monster")
			}
			seq++
			if _, err := a.execute("/PackInGameInfo", trapRequest(seq, 21)); err == nil || !strings.Contains(err.Error(), "cannot change pack during active battle") {
				t.Fatalf("resume allowed an unrelated story pack: %v", err)
			}
			// Native restoration enters the castle lobby before replaying the turn.
			call("/EvilCastleEnter", appScalar(2, 3003))
			call("/PackInGameInfo", appScalar(2, 3003))
			call("/BattleStart", start)
		}
		ended := call("/BattleEnd", append(appScalar(2, 1), appMessage(3, final)...))
		floor := appRows(ended, 10)
		if len(floor) != 1 {
			t.Fatal("ordinary encounters also need native score data")
		}
		score = appValue(floor[0], 4)
		if score == 0 {
			t.Fatal("missing in-progress score")
		}
		a.reopen(t)
		info := call("/EvilCastleInfo", nil)
		if monster != 210 {
			if len(appRows(info, 1)) != 0 || appValue(appRows(info, 2)[0], 2) != 0 {
				t.Fatal("unfinished floor exposed completed score")
			}
			if appValue(call("/EvilCastleDailyRewardState", nil), 1) != 0 {
				t.Fatal("unfinished score qualified for daily reward")
			}
		} else {
			rows := appRows(info, 1)
			if len(rows) != 1 || appValue(rows[0], 5) != score {
				t.Fatal("boss did not persist complete floor")
			}
		}
	}
	currency := func() uint64 {
		t.Helper()
		var amount uint64
		a.run(t, false, func(ctx command.Context, p *playerAssembly) {
			var err error
			amount, err = p.eventEconomy.CurrencyBalance(ctx, 18)
			if err != nil {
				t.Fatal(err)
			}
		})
		return amount
	}
	before := currency()
	seq++
	claim := append(appScalar(1, seq), appScalar(2, 3003)...)
	claim = append(claim, appScalar(3, 1)...)
	result := a.protocol(t, "/EvilCastleStageClearReward", claim)
	if !bytes.Equal(result, a.protocol(t, "/EvilCastleStageClearReward", claim)) {
		t.Fatal("claim retry changed response")
	}
	a.reopen(t)
	if currency() != before+1000 {
		t.Fatal("first-floor 1000 currency reward not credited exactly once")
	}
	rows := appRows(call("/EvilCastleInfo", nil), 1)
	if len(rows) != 1 || appValue(rows[0], 6) != 1 {
		t.Fatal("claimed reward state lost after reopen")
	}
}

// Current GameData first Rage/Envy floors each award 30000 gold for victory,
// 30000 for no deaths, and 5 currency33 for winning within eight turns.
func TestRageAndEnvyTowerChallengesOnlyPayNewVictories(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	a := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, a.owner) })
	var blue, final []byte
	a.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.ownedItems.GrantOnce(ctx, "tower-ticket", []gamedata.BattleReward{{Type: 19, ID: 13003, Count: 1}}); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.worldService.CharacterService().RawAll() {
			if roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) {
				continue
			}
			costume, ok := p.collection.CostumeByIndex(c.UseCostume)
			if !ok {
				continue
			}
			hp, err := p.worldService.CharacterService().MaxHealth(ctx, c.InvenIndex)
			if err != nil {
				t.Fatal(err)
			}
			blue = appScalar(1, 1)
			for _, f := range [][2]uint64{{2, c.InvenIndex}, {3, c.ID}, {4, hp}, {7, costume.ID}} {
				blue = append(blue, appScalar(int(f[0]), f[1])...)
			}
			final = append(appScalar(1, c.InvenIndex), appScalar(2, c.ID)...)
			final = append(final, appScalar(3, hp)...)
			break
		}
		if len(blue) == 0 {
			t.Fatal("missing owned participant")
		}
	})
	a.reopen(t)
	seq := uint64(0)
	call := func(path string, body []byte) []byte {
		t.Helper()
		seq++
		return a.protocol(t, path, append(appScalar(1, seq), body...))
	}
	for _, tc := range []struct{ mode, deck uint64 }{{19, 20001}, {20, 30001}} {
		t.Run(fmt.Sprintf("mode%d", tc.mode), func(t *testing.T) {
			balance := func() (uint64, uint64) {
				t.Helper()
				var gold, ap uint64
				a.run(t, false, func(ctx command.Context, p *playerAssembly) {
					gold = p.wallet.Snapshot(ctx).Gold
					var err error
					ap, err = p.eventEconomy.CurrencyBalance(ctx, 33)
					if err != nil {
						t.Fatal(err)
					}
				})
				return gold, ap
			}
			beforeGold, beforeAP := balance()
			play := func(challenges []uint64) {
				t.Helper()
				call("/PackInGameInfo", appScalar(2, 3003))
				enter := append(appScalar(4, tc.deck), appScalar(5, tc.mode)...)
				enter = append(enter, appScalar(9, 1)...)
				call("/BattleEnter", enter)
				call("/BattleStart", wire.AppendBytes(appScalar(2, tc.deck), 5, blue))
				end := wire.AppendBytes(appScalar(2, 1), 3, final)
				for _, id := range challenges {
					end = wire.AppendVarint(end, 5, id)
				}
				seq++
				request := append(appScalar(1, seq), end...)
				result := a.protocol(t, "/BattleEnd", request)
				if !bytes.Equal(result, a.protocol(t, "/BattleEnd", request)) {
					t.Fatal("settlement retry changed reward response")
				}
				a.reopen(t)
			}
			play([]uint64{0})
			if gold, ap := balance(); gold != beforeGold+30000 || ap != beforeAP {
				t.Fatal("first victory reward differs from current GameData")
			}
			play([]uint64{0, 1, 2})
			if gold, ap := balance(); gold != beforeGold+60000 || ap != beforeAP+5 {
				t.Fatal("new challenges did not pay their incremental rewards")
			}
			play([]uint64{0, 1, 2})
			if gold, ap := balance(); gold != beforeGold+60000 || ap != beforeAP+5 {
				t.Fatal("repeat clear paid completed challenges again")
			}
			var found bool
			for _, r := range appRows(call("/EvilCastleTowerInfo", nil), 1) {
				if appValue(r, 1) == 1 && appValue(r, 3) == tc.mode {
					found = true
				}
			}
			if !found {
				t.Fatal("tower progress lost across reopen")
			}
		})
	}
}
