//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func TestCostumeOverflowPaysCompensationWithoutRepeatingCollectionNotice(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	// Each scenario owns its account: trap commands restart their request IDs,
	// so sharing one account would replay the previous scenario's receipts.
	for i, scenario := range []struct {
		name              string
		costume           uint64
		goldThreadPerCopy uint64
	}{
		{"starter", 64701, 2},
		{"earned", 1401, 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s := &trapPlayerScenario{factory: factory, account: accounts[i], owner: openNicknamePlayer(t, factory, accounts[i])}
			t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
			grant := func(key string, count uint64) []byte {
				t.Helper()
				var bundle []byte
				s.run(t, true, func(ctx command.Context, p *playerAssembly) {
					var err error
					bundle, err = p.eventEconomy.Apply(ctx, scenario.name+":"+key, nil, []gamedata.Reward{{Type: 11, ID: scenario.costume, Count: count}})
					if err != nil {
						t.Fatal(err)
					}
				})
				return bundle
			}
			costume, owned := s.owner.assembly.collection.CostumeByID(scenario.costume)
			if scenario.name == "earned" {
				if owned {
					t.Fatal("new costume is already owned before its first reward")
				}
				rows := appRows(grant("first-copy", 1), 3)
				if len(rows) != 1 || appValue(rows[0], 2) != scenario.costume {
					t.Fatal("first copy did not deliver the costume")
				}
				costume, owned = s.owner.assembly.collection.CostumeByID(scenario.costume)
			}
			if !owned || costume.Level > 4 {
				t.Fatal("costume cannot exercise the actual final enhancement")
			}
			for level := costume.Level; level < 4; level++ {
				grant(fmt.Sprintf("enhance-%d", level), 1)
			}
			upgrade := appRows(grant("final-enhancement", 1), 9)
			if len(upgrade) != 1 || appValue(upgrade[0], 4) != 4 || appValue(upgrade[0], 5) != 5 {
				t.Fatal("a genuine +4 to +5 enhancement lost its collection notification")
			}
			s.reopen(t)
			book := s.protocol(t, "/PictorialBookInfo", appScalar(1, uint64(s.commands+1)))
			before := s.owner.assembly.wallet.Snapshot(command.Context{}).Mileage
			bundle := grant("two-overflow-copies", 2)
			if len(appRows(bundle, 9)) != 0 || len(appRows(bundle, 2)) != 0 || len(appRows(bundle, 3)) != 0 {
				t.Fatal("overflow falsely announced new ownership or an enhancement")
			}
			exchanges := appRows(bundle, 8)
			if len(exchanges) != 2 {
				t.Fatal("two overflow copies did not produce their compensation records")
			}
			for _, exchange := range exchanges {
				if appValue(exchange, 1) != 11 || appValue(exchange, 2) != scenario.costume || appValue(exchange, 4) != 20 || appValue(exchange, 6) != scenario.goldThreadPerCopy {
					t.Fatal("overflow compensation differs from CostumeGrowthTable's gold thread reward")
				}
			}
			s.reopen(t)
			if !bytes.Equal(bundle, grant("two-overflow-copies", 2)) {
				t.Fatal("reward retry changed the committed overflow receipt")
			}
			s.reopen(t)
			costume, _ = s.owner.assembly.collection.CostumeByID(scenario.costume)
			if costume.Level != 5 || s.owner.assembly.wallet.Snapshot(command.Context{}).Mileage != before+2*scenario.goldThreadPerCopy {
				t.Fatal("overflow retry changed enhancement or credited compensation twice")
			}
			if !bytes.Equal(book, s.protocol(t, "/PictorialBookInfo", appScalar(1, uint64(s.commands+1)))) {
				t.Fatal("overflow changed the already completed collection bonus")
			}
		})
	}
}

// NewsUI uses CommunityWebLinkTable's displayed rewards and NoticePacket restores
// claim markers before enabling the same buttons after login.
func TestCommunityRewardCreditsOnceAndRestoresPerAccountClaims(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	players := make([]*trapPlayerScenario, 2)
	for i, account := range accounts {
		s := &trapPlayerScenario{factory: factory, account: account, owner: openNicknamePlayer(t, factory, account)}
		players[i] = s
		t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	}
	a, b := players[0], players[1]
	before := a.owner.assembly.wallet.Snapshot(command.Context{}).FreeJewelry
	request := trapRequest(1, 1)
	result := a.protocol(t, "/CommunityReward", request)
	rewards := appRows(appRows(result, 1)[0], 1)
	var displayed uint64
	for _, reward := range rewards {
		if appValue(reward, 3) == 3 {
			displayed += appValue(reward, 4)
		}
	}
	// CommunityWebLinkTable 0 in GameData 20261021182514 advertises 100 free diamonds.
	if displayed != 100 || a.owner.assembly.wallet.Snapshot(command.Context{}).FreeJewelry != before+100 {
		t.Fatal("community reward shown by the client was not credited to spendable diamonds")
	}
	a.protocol(t, "/CommunityReward", trapRequest(2, 1))
	a.reopen(t)
	rows := appRows(a.protocol(t, "/CommunityRewardInfo", appScalar(1, 3)), 1)
	if len(rows) != 1 || appValue(rows[0], 1) != 1 {
		t.Fatal("relogin made the already-claimed community button claimable again")
	}
	a.protocol(t, "/CommunityReward", trapRequest(4, 1))
	if got := a.owner.assembly.wallet.Snapshot(command.Context{}).FreeJewelry; got != before+displayed {
		t.Fatal("community reward replay credited diamonds again")
	}
	if rows := appRows(b.protocol(t, "/CommunityRewardInfo", appScalar(1, 1)), 1); len(rows) != 0 {
		t.Fatal("one player's community claims disabled another player's buttons")
	}
	_, err := a.execute("/CommunityReward", wire.AppendVarint(trapRequest(5, 0), 3, 999999))
	if err == nil {
		t.Fatal("a nonexistent survey granted its displayed reward")
	}
}

// GameData's InteractionAchievementTable gives illustration 1065193 point 1/1
// independent motions 0..4. Client saves only new motions, not a full snapshot.
func TestInteractionDeltasSurviveRelogWithoutErasingEarlierDiscoveries(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.ownedItems.GrantOnce(ctx, "interaction-owned-illustration", []gamedata.BattleReward{{Type: 45, ID: 65193, Count: 1}}); err != nil {
			t.Fatal(err)
		}
	})
	save := func(motion uint64) []byte {
		point := wire.AppendVarint(nil, 1, 1)
		point = wire.AppendVarint(point, 2, motion)
		group := wire.AppendVarint(nil, 1, 1065193)
		group = wire.AppendVarint(group, 2, 1)
		group = wire.AppendBytes(group, 3, point)
		return wire.AppendBytes(appScalar(1, uint64(s.commands+1)), 2, group)
	}
	s.protocol(t, "/SpineInteractionAchievementSave", save(0))
	s.reopen(t)
	s.protocol(t, "/SpineInteractionAchievementSave", save(1))
	s.protocol(t, "/SpineInteractionAchievementSave", save(0))
	s.reopen(t)
	assertMotions := func() {
		t.Helper()
		groups := appRows(s.protocol(t, "/SpineInteractionAchievementInfo", appScalar(1, uint64(s.commands+1))), 1)
		if len(groups) != 1 || appValue(groups[0], 1) != 1065193 || appValue(groups[0], 2) != 1 {
			t.Fatal("relogin did not restore the illustration's discovered group")
		}
		points := appRows(groups[0], 3)
		var motions []uint64
		if len(points) != 1 || appValue(points[0], 1) != 1 {
			t.Fatal("saved discoveries appeared on another interaction point")
		}
		if err := wire.Walk(points[0], func(f wire.Field) error {
			if f.Number == 2 {
				if f.Type != 0 && f.Type != 2 {
					return wire.ErrMalformed
				}
				for raw := f.Value; len(raw) > 0; {
					value, count := binary.Uvarint(raw)
					if count <= 0 {
						return wire.ErrMalformed
					}
					motions = append(motions, value)
					raw = raw[count:]
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(motions, []uint64{0, 1}) {
			t.Fatalf("incremental save or retry lost/doubled prior motion discoveries: %v", motions)
		}
	}
	assertMotions()
	if _, err := s.execute("/SpineInteractionAchievementSave", save(999999)); err == nil {
		t.Fatal("an unlisted motion was accepted as discovered")
	}
	if err := s.owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertMotions()
	other := &trapPlayerScenario{factory: factory, account: accounts[1], owner: openNicknamePlayer(t, factory, accounts[1])}
	defer closeNicknamePlayer(t, other.owner)
	if rows := appRows(other.protocol(t, "/SpineInteractionAchievementInfo", appScalar(1, 1)), 1); len(rows) != 0 {
		t.Fatal("another player's private interaction discoveries leaked")
	}
}

// TotalWarPacket registers owned costumes before sending their damage. Its
// persistent best result controls the daily reward button across later logins.
func TestTotalWarResultRestoresDailyRewardWithoutDuplicatingInventory(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	var participant []byte
	var costumeID uint64
	for _, costume := range s.owner.assembly.collection.Costumes() {
		definition, found := factory.design.regularGacha.Character(costume.ID)
		if !found {
			continue
		}
		for _, character := range s.owner.assembly.worldService.CharacterService().RawAll() {
			if definition.ID != character.ID && !slices.Contains(definition.GrowthCharacterIDs, character.ID) {
				continue
			}
			participant = appScalar(1, 1)
			participant = append(participant, appScalar(2, character.InvenIndex)...)
			participant = append(participant, appScalar(3, character.ID)...)
			participant = append(participant, appScalar(4, character.HP)...)
			participant = append(participant, appScalar(5, max(character.Level, 1))...)
			participant = append(participant, appScalar(6, costume.InvenIndex)...)
			participant = append(participant, appScalar(7, costume.ID)...)
			costumeID = costume.ID
			break
		}
		if costumeID != 0 {
			break
		}
	}
	if costumeID == 0 {
		t.Fatal("starter roster cannot participate in total war")
	}
	deckEntry := appScalar(2, appValue(participant, 6))
	deckEntry = append(deckEntry, appScalar(3, appValue(participant, 2))...)
	s.protocol(t, "/TotalWarDeckSave", wire.AppendBytes(appScalar(1, 100), 2, deckEntry))
	deckBody := s.protocol(t, "/TotalWarDeckInfo", appScalar(1, 101))
	for _, contents := range appRows(deckBody, 2) {
		if appValue(contents, 1) != appValue(participant, 2) {
			continue
		}
		participant = append(participant, appScalar(29, appValue(contents, 3))...)
		for _, ref := range appRows(contents, 2) {
			if id := appValue(ref, 1); id != 0 {
				participant = append(participant, appScalar(12, id)...)
			}
		}
	}
	// BattlePlayData.SetPlayDataTotalWar reads pack3005 BattleDeck 1 and sets
	// its monster HP to one million before TotalWarPacket sends the roster.
	enemies := []byte{}
	for i, enemy := range factory.design.castle.TotalWarEnemies {
		red := appScalar(1, uint64(i+100))
		red = append(red, appScalar(3, enemy.ID)...)
		red = append(red, appScalar(4, 1000000)...)
		red = append(red, appScalar(5, enemy.Level)...)
		red = append(red, appScalar(9, uint64(i))...)
		enemies = wire.AppendBytes(enemies, 4, red)
	}
	start := append(wire.AppendBytes(trapRequest(1, 1), 3, participant), enemies...)
	s.protocol(t, "/TotalWarBattleStart", start)
	s.reopen(t)
	// Current TotalWarRewardTable's first tier starts at 10,000 damage and
	// grants auto-open box 905001 (gold 2000 + material 603 x1).
	end := wire.AppendBytes(appScalar(1, 2), 2, trapRequest(costumeID, 10000))
	s.protocol(t, "/TotalWarBattleEnd", end)
	s.reopen(t)
	if appValue(s.protocol(t, "/TotalWarRewardState", appScalar(1, 3)), 1) != 1 {
		t.Fatal("a committed qualifying result lost its unclaimed daily reward on login")
	}
	before := appItemTotals(s.protocol(t, "/ItemInfo", appScalar(1, 4)))[[2]uint64{8, 603}]
	goldBefore := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	s.protocol(t, "/TotalWarReward", appScalar(1, 5))
	after := appItemTotals(s.protocol(t, "/ItemInfo", appScalar(1, 6)))[[2]uint64{8, 603}]
	if after != before+1 || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != goldBefore+2000 {
		t.Fatal("total war daily reward did not credit its authored gold and material")
	}
	s.reopen(t)
	if appValue(s.protocol(t, "/TotalWarRewardState", appScalar(1, 7)), 1) != 0 {
		t.Fatal("claimed total war daily reward became claimable again after login")
	}
	if _, err := s.execute("/TotalWarReward", appScalar(1, 8)); err == nil {
		t.Fatal("a new request claimed the same day's reward twice")
	}
	if err := s.owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if appItemTotals(s.protocol(t, "/ItemInfo", appScalar(1, 9)))[[2]uint64{8, 603}] != after || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != goldBefore+2000 {
		t.Fatal("rejected repeated daily claim changed owned rewards")
	}
	s.protocol(t, "/TotalWarBattleStart", append(wire.AppendBytes(trapRequest(10, 1), 3, participant), enemies...))
	s.protocol(t, "/TotalWarBattleEnd", wire.AppendBytes(appScalar(1, 11), 2, trapRequest(costumeID, 1)))
	s.reopen(t)
	result := s.protocol(t, "/TotalWarInfo", appScalar(1, 12))
	rows := appRows(result, 1)
	if len(rows) != 1 || appValue(rows[0], 1) != costumeID || appValue(rows[0], 2) != 10000 {
		t.Fatal("replaying a lower score replaced the committed best result")
	}
	other := &trapPlayerScenario{factory: factory, account: accounts[1], owner: openNicknamePlayer(t, factory, accounts[1])}
	defer closeNicknamePlayer(t, other.owner)
	if len(other.protocol(t, "/TotalWarInfo", appScalar(1, 1))) != 0 {
		t.Fatal("total war scores crossed account boundaries")
	}
}

func TestExternalLifeRewardsAndShopConsumeTheSamePersistedBalance(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	var group, id, price, itemID, quantity uint64
	for key, row := range factory.design.life.Tables["LifeBuyItemTable"] {
		// LifeShopUI sends group/id/count; LifeBuyItemTable defines the life-coin
		// payment and life-material quantity displayed by that shop entry.
		if appValue(row, 8) == 64 && appValue(row, 5) == 66 && appValue(row, 6) != 0 {
			group, id, price = key[0], key[1], appValue(row, 6)
			itemID, quantity = appValue(row, 4), appValue(row, 3)
			break
		}
	}
	if price == 0 || quantity == 0 {
		t.Fatal("current life shop has no coin-funded material purchase")
	}
	grant := func(ctx command.Context, p *playerAssembly) {
		t.Helper()
		if _, err := p.eventEconomy.Apply(ctx, "external-life-reward", nil, []gamedata.Reward{{Type: 64, Count: price * 2}}); err != nil {
			t.Fatal(err)
		}
	}
	s.run(t, true, grant)
	request := trapRequest(1, group)
	request = wire.AppendVarint(request, 3, id)
	request = wire.AppendVarint(request, 4, 1)
	s.protocol(t, "/LifeShopBuy", request)
	s.reopen(t)
	// Replaying the external entitlement after spending must not refill coins.
	s.run(t, true, grant)
	profile := s.protocol(t, "/LifeUserInfo", appScalar(1, 2))
	users := appRows(profile, 2)
	if len(users) != 1 || appValue(users[0], 1) != price {
		t.Fatal("external reward and life shop used different balances or replay refilled spent coins")
	}
	info := s.protocol(t, "/LifeInfo", appScalar(1, 3))
	var acquired uint64
	for _, item := range appRows(info, 3) {
		if appValue(item, 2) == itemID && appValue(item, 3) == 66 {
			acquired += appValue(item, 4)
		}
	}
	if acquired != quantity {
		t.Fatal("spent life coins did not restore the purchased materials after database reopen")
	}
	other := &trapPlayerScenario{factory: factory, account: accounts[1], owner: openNicknamePlayer(t, factory, accounts[1])}
	defer closeNicknamePlayer(t, other.owner)
	otherUsers := appRows(other.protocol(t, "/LifeUserInfo", appScalar(1, 1)), 2)
	if len(otherUsers) != 1 || appValue(otherUsers[0], 1) != 0 {
		t.Fatal("external life rewards changed another account's life-coin balance")
	}
}
