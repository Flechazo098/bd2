//go:build integration

package app

import (
	"context"
	"fmt"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func avatarChoiceRequest(shop uint64) []byte {
	price := wire.AppendVarint(wire.AppendVarint(nil, 3, 44), 4, 1000)
	return wire.AppendBytes(nil, 2, wire.AppendBytes(wire.AppendVarint(nil, 1, shop), 2, price))
}

// AvatarSelectUI -> SendFirstBuyAvatarCharRequest submits shop1's nominal
// price without a funds check. AvatarPacket first clamps the local debit to
// zero, then adds the response bundle. First choice is free by server policy.
func TestSquareFirstChoiceWorksWithoutFundsAndKeepsClientBalanceConsistent(t *testing.T) {
	for _, initial := range []uint64{0, 500, 2500} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			g := profileGameplay(t)
			if initial > 0 {
				profileGrant(g, gamedata.Reward{Type: 44, Count: initial})
			}
			before := g.wordCurrency()
			response := g.call("/AvatarShopBuy", avatarChoiceRequest(1))
			bundle, _, err := wire.Bytes(response, 1)
			if err != nil {
				t.Fatal(err)
			}
			localAfterDebit := before - min(before, uint64(1000))
			for _, item := range appRows(bundle, 1) {
				if appValue(item, 3) == 44 {
					localAfterDebit += appValue(item, 4)
				}
			}
			if localAfterDebit != before || g.wordCurrency() != before {
				t.Fatalf("free choice desynchronized native/server currency: native=%d server=%d before=%d", localAfterDebit, g.wordCurrency(), before)
			}
			for _, asset := range [][2]uint64{{61, 1}, {49, 10001}, {49, 20001}, {49, 40001}, {19, 3010}, {19, 3011}} {
				if g.itemCount(asset[0], asset[1]) != 1 {
					t.Fatalf("first choice failed to deliver avatar or SquareDefault unlock %v", asset)
				}
			}
			use := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 10001), 3, 20001), 5, 40001)
			g.call("/AvatarSave", wire.AppendBytes(nil, 2, use))
			g.reopen()
			avatar := g.call("/AvatarInfo", nil)
			current, _, err := wire.Bytes(avatar, 1)
			if err != nil || appValue(current, 1) != 1 || appValue(current, 5) != 40001 {
				t.Fatalf("equipped initial avatar did not survive reopening: %v", err)
			}
			login := g.call("/LoginUser", nil)
			user, _, err := wire.Bytes(login, 1)
			if err != nil || appValue(user, 56) != 1 {
				t.Fatalf("login lost consumed first-choice eligibility: %v", err)
			}
			if before < 1000 {
				g.execute(g.request("/AvatarShopBuy", avatarChoiceRequest(2)), true)
				if g.itemCount(61, 2) != 0 || g.wordCurrency() != before {
					t.Fatal("second character bypassed ordinary purchase price")
				}
			} else {
				g.call("/AvatarShopBuy", avatarChoiceRequest(2))
				if g.itemCount(61, 2) != 1 || g.wordCurrency() != before-1000 {
					t.Fatal("second character was not charged at the authored price")
				}
			}
			if g.itemCount(19, 3010) != 1 || g.itemCount(19, 3011) != 1 {
				t.Fatal("later purchase duplicated setup unlocks")
			}
			// CommonPacket.EnterPack uses the shared card endpoints for both
			// Fishing(12) and Life(13), after their domain initialization calls.
			for _, entry := range []struct {
				id   uint64
				info string
			}{{3010, "/FishingInfo"}, {3011, "/LifeInfo"}} {
				fields := wire.AppendVarint(nil, 2, entry.id)
				g.call("/PackPreviewInfo", fields)
				g.call(entry.info, nil)
				g.call("/PackInGameInfo", fields)
			}
		})
	}
}

func TestFailedInitialAvatarBatchDoesNotConsumeTheFreeChoice(t *testing.T) {
	g := profileGameplay(t)
	profileGrant(g, gamedata.Reward{Type: 44, Count: 500})
	buy := g.request("/AvatarShopBuy", avatarChoiceRequest(1))
	unowned := wire.AppendBytes(nil, 2, wire.AppendVarint(nil, 1, 18))
	invalid := g.request("/AvatarSave", unowned)
	buy.Requests = append(buy.Requests, invalid.Requests...)
	g.execute(buy, true)
	g.reopen()
	if g.wordCurrency() != 500 || g.itemCount(61, 1) != 0 || g.itemCount(19, 3010) != 0 || g.itemCount(19, 3011) != 0 {
		t.Fatal("rejected initial setup retained some purchase/entitlement state")
	}
	g.call("/AvatarShopBuy", avatarChoiceRequest(2))
	if g.wordCurrency() != 500 || g.itemCount(61, 2) != 1 || g.itemCount(19, 3010) != 1 || g.itemCount(19, 3011) != 1 {
		t.Fatal("rolled-back setup consumed the one free choice")
	}
}

// Pack3009 BuyRewardType/Id/Count and RandomBox930000 -> RewardGroup930000
// supply ticket3009 plus all32 authored motions. Opening/previewing a card is
// not a purchase; purchase receipts must survive reopening without regranting.
func TestSquareCardPurchaseDeliversItsAuthoredRewardsOnce(t *testing.T) {
	g := profileGameplay(t)
	profileGrant(g, gamedata.Reward{Type: 19, ID: 11, Count: 1})
	request := wire.AppendVarint(nil, 2, 3009)
	before := g.itemCount(19, 3009)
	g.call("/PackPreviewInfo", request)
	if g.itemCount(19, 3009) != before {
		t.Fatal("card preview granted purchase rewards")
	}
	response := g.call("/PackBuy", request)
	info, _, err := wire.Bytes(response, 1)
	if err != nil || appValue(info, 1) != 3009 || appValue(info, 8) != 1 {
		t.Fatalf("purchase did not mark the plaza card as owned: %v", err)
	}
	if g.itemCount(19, 3009) != before+1 {
		t.Fatal("plaza purchase omitted its content ticket")
	}
	var motions []uint64
	for id := uint64(1); id <= 17; id++ {
		motions = append(motions, id)
	}
	for id := uint64(100001); id <= 100015; id++ {
		motions = append(motions, id)
	}
	for _, id := range motions {
		if g.itemCount(50, id) != 1 {
			t.Fatalf("authored plaza entry motion %d not granted", id)
		}
	}
	g.reopen()
	g.call("/PackBuy", request)
	g.call("/PackInGameInfo", request)
	for _, id := range motions {
		if g.itemCount(50, id) != 1 {
			t.Fatalf("reopening/reentry duplicated motion %d", id)
		}
	}
	if g.itemCount(19, 3009) != before+1 {
		t.Fatal("reopening/reentry duplicated plaza ticket")
	}
}

// GameData 20261021182514 Pack3006 is PackGuildRaid (8), with maps30061+
// and a one-time purchase reward of 1000 gold. CommonPacket buys it through
// the same PackBuy callback as other field cards before GuildRaid entry.
func TestGuildRaidCardPurchasePersistsAndCannotRepeatItsReward(t *testing.T) {
	g := profileGameplay(t)
	request := wire.AppendVarint(nil, 2, 3006)
	// Exercise the domain transaction without the login observer purchasing
	// unrelated open cards and adding their gold to the same command.
	call := func(path string, body []byte) []byte {
		t.Helper()
		tx, err := g.owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		ctx := command.Context{Identity: command.Identity{AccountID: g.account}, State: tx, InstanceIDs: g.factory.instanceIDs()}
		_, response, handled, err := g.owner.assembly.worldService.Handle(ctx, path, append(wire.AppendVarint(nil, 1, 1), body...))
		if err != nil || !handled {
			t.Fatalf("%s: handled=%v error=%v", path, handled, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return response
	}
	goldBefore := g.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	response := call("/PackBuy", request)
	info, _, err := wire.Bytes(response, 1)
	if err != nil || appValue(info, 1) != 3006 || appValue(info, 8) != 1 {
		t.Fatalf("guild raid card purchase did not return owned card: %v", err)
	}
	bundle, _, err := wire.Bytes(response, 2)
	if err != nil {
		t.Fatal(err)
	}
	var gold uint64
	for _, item := range appRows(bundle, 1) {
		if appValue(item, 3) == 4 {
			gold += appValue(item, 4)
		}
	}
	if gold != 1000 {
		t.Fatalf("guild raid card purchase omitted authored gold reward: %d", gold)
	}
	if g.owner.assembly.wallet.Snapshot(command.Context{}).Gold != goldBefore+1000 {
		t.Fatal("guild raid purchase returned gold without crediting the wallet")
	}
	g.reopen()
	call("/PackBuy", request)
	call("/PackPreviewInfo", request)
	if g.owner.assembly.wallet.Snapshot(command.Context{}).Gold != goldBefore+1000 {
		t.Fatal("reopening or previewing guild raid card repeated its purchase reward")
	}
	var owned bool
	for _, row := range appRows(call("/PackInfo", nil), 1) {
		if appValue(row, 1) == 3006 && appValue(row, 8) == 1 {
			owned = true
		}
	}
	if !owned {
		t.Fatal("guild raid card ownership lost after reopening")
	}
}
