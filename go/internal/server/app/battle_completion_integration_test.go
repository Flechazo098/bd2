//go:build integration

package app

import (
	"bytes"
	"testing"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/runtime/player"
)

// LocalText457/469 and FriendShipWeightPopupUI describe partner efficiency;
// current Lathel->Justia is a directed pair with base10 and10% bonus per win.
func TestQuestVictoryPersistsPartnerProgressAndRetryCannotDuplicateIt(t *testing.T) {
	g := newInventoryGameplay(t)
	var entered []roster.Character
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.collection.GrantCostumes(ctx, "battle-partners", []uint64{101, 201}, p.design.regularGacha); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.worldService.CharacterService().RawAll() {
			if !roster.IsStoryCharacter(c) && !roster.IsCharmCharacter(c) && (c.ID == 10 || c.ID == 20) {
				entered = append(entered, c)
			}
		}
		if len(entered) != 2 {
			t.Fatal("battle fixture requires owned Lathel and Justia")
		}
		if err := p.progressState.SetActivePackID(ctx, 1); err != nil {
			t.Fatal(err)
		}
		for quest := 1; quest < 9; quest++ {
			if err := p.progressState.ClearQuest(ctx, quest, 1, 0); err != nil {
				t.Fatal(err)
			}
		}
	})
	g.reopen()
	enter := append(appScalar(3, 1), appScalar(4, 1)...)
	enter = append(enter, appScalar(5, 1)...)
	g.call("/BattleEnter", enter)
	start := appScalar(2, 1)
	for i, c := range entered {
		deck := append(appScalar(1, c.InvenIndex), appScalar(2, uint64(i))...)
		deck = append(deck, appScalar(3, uint64(i+1))...)
		start = append(start, appMessage(3, deck)...)
		character := append(appScalar(2, c.InvenIndex), appScalar(3, 100)...)
		character = append(character, appScalar(4, 100)...)
		start = append(start, appMessage(5, character)...)
	}
	g.call("/BattleStart", start)
	partnerPoint := func() uint64 {
		t.Helper()
		for _, row := range appRows(g.call("/CharPartnerInfo", nil), 1) {
			if appValue(row, 1) == 1 && appValue(row, 2) == 2 {
				return appValue(row, 3)
			}
		}
		return 0
	}
	if partnerPoint() != 0 {
		t.Fatal("starting a battle granted partner progress before victory")
	}
	failed := g.request("/BattleEnd", appScalar(2, 1))
	failed.Requests = append(failed.Requests, player.Request{Path: "/ItemDump", Body: append(appScalar(1, 900), appMessage(2, inventoryItem(999999, 1, 8, 1))...)})
	g.execute(failed, true)
	g.reopen()
	if partnerPoint() != 0 {
		t.Fatal("failed settlement batch committed partner progress")
	}
	end := g.request("/BattleEnd", appScalar(2, 1))
	reply := g.execute(end, false)
	partners := appRows(reply, 9)
	if len(partners) != 1 || appValue(partners[0], 3) != 11 || partnerPoint() != 11 {
		t.Fatal("victory did not persist the partner gain consumed by the client")
	}
	g.reopen()
	if partnerPoint() != 11 {
		t.Fatal("partner gain disappeared after reopening account")
	}
	if retry := g.execute(end, false); !bytes.Equal(retry, reply) || partnerPoint() != 11 {
		t.Fatal("settlement replay duplicated partner progress or changed response")
	}
}
