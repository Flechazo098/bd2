//go:build integration

package app

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

// Current 2.36.7 PackInfoResponse fields 3/4 rebuild CashShopPacket's claimed
// lists. GameData 20261008155037 ClearPackTable group 1, pack 1 has free ticket
// 12 and premium ticket 440000. RewardGroupTable 440001/440011 deliver 100/2000
// free diamonds through cash mail templates 562/563 (gamedata_db.py queries).
func TestClearPackageClaimsSurviveSessionAndDayChanges(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	account := accounts[0]
	owner, err := factory.open(account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if owner != nil {
			if err := owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	sequence := uint64(0)
	call := func(path string, fields []byte) []byte {
		t.Helper()
		sequence++
		body := append(appScalar(1, sequence), fields...)
		request := registryCommand(account, fmt.Sprintf("clear-package-%d", sequence), path, body)
		request.Identity.SessionID = fmt.Sprintf("clear-package-session-%d", sequence)
		if path == "/LoginUser" {
			request.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
		}
		reply, err := owner.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(reply.Responses) != 1 {
			t.Fatalf("%s: got %d responses", path, len(reply.Responses))
		}
		return reply.Responses[0].Body
	}
	assertClaims := func(tickets ...uint64) {
		t.Helper()
		response := call("/PackInfo", nil)
		if len(appRows(response, 1)) == 0 {
			t.Fatal("PackInfo lost world pack rows while projecting claims")
		}
		rows := appRows(response, 3)
		if len(rows) != len(tickets) {
			t.Fatalf("PackInfo projected %d story claims, want %d", len(rows), len(tickets))
		}
		var got []uint64
		for _, row := range rows {
			if appValue(row, 1) != 1 || appValue(row, 3) != 1 || appValue(row, 4) != 0 {
				t.Fatalf("PackInfo returned a different claim identity: %x", row)
			}
			got = append(got, appValue(row, 2))
		}
		slices.Sort(got)
		slices.Sort(tickets)
		if !slices.Equal(got, tickets) || len(appRows(response, 4)) != 0 {
			t.Fatalf("PackInfo mixed claim tickets or kinds: got %v, want %v", got, tickets)
		}
	}
	claim := func(ticket uint64) {
		t.Helper()
		row := append(appScalar(1, 1), appScalar(2, ticket)...)
		row = append(row, appScalar(3, 1)...)
		call("/ClearPackageReward", append(appMessage(3, row), appMessage(4, nil)...))
	}
	freeDiamonds := func() uint64 {
		t.Helper()
		tx, err := owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		return owner.assembly.wallet.Snapshot(command.Context{State: tx}).FreeJewelry
	}
	assertCashMail := func(template, count uint64) uint64 {
		t.Helper()
		var matches [][]byte
		for _, path := range []string{"/CashMailInfo", "/MailHistoryInfo"} {
			response := call(path, appScalar(3, 100))
			for _, row := range appRows(response, 1) {
				if appValue(row, 3) == template {
					matches = append(matches, row)
				}
			}
		}
		if len(matches) != 1 {
			t.Fatalf("cash mail template %d: got %d deliveries, want one", template, len(matches))
		}
		row := matches[0]
		if !slices.Equal(appPacked(row, 8), []uint64{3}) || !slices.Equal(appPacked(row, 9), []uint64{0}) || !slices.Equal(appPacked(row, 10), []uint64{count}) {
			t.Fatalf("cash mail template %d has wrong attachments: %x", template, row)
		}
		return appValue(row, 1)
	}

	assertClaims()
	// Seed completed story progress and ownership, retaining the real progress
	// verifier, ticket inventory, reward delivery, transaction and route chain.
	tx, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{Identity: command.Identity{AccountID: account}, State: tx, InstanceIDs: factory.instanceIDs()}
	for _, quest := range factory.design.world.Story.Packs[1].MainQuestIDs {
		if err := owner.assembly.progressState.ClearQuest(ctx, quest, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.assembly.ownedItems.GrantOnce(ctx, "clear-package-premium-fixture", []gamedata.BattleReward{{Type: 19, ID: 440000, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	before := freeDiamonds()
	claim(12)
	assertClaims(12)
	freeMail := assertCashMail(562, 100)
	call("/MailOpen", appScalar(2, freeMail))
	if got := freeDiamonds(); got != before+100 {
		t.Fatalf("free clear claim credited %d diamonds, want %d", got, before+100)
	}
	claim(440000)
	assertClaims(12, 440000)
	premiumMail := assertCashMail(563, 2000)
	call("/MailOpen", appScalar(2, premiumMail))
	if got := freeDiamonds(); got != before+2100 {
		t.Fatalf("premium clear claim credited %d diamonds, want %d", got, before+2100)
	}

	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner = nil
	owner, err = factory.open(account)
	if err != nil {
		t.Fatal(err)
	}
	tomorrow := time.Now().AddDate(0, 0, 1)
	clock := func() time.Time { return tomorrow }
	reset := factory.design.eventAPReset.ResetSeconds - 9*3600
	owner.assembly.cashService.SetClock(clock, reset)
	owner.assembly.cashEconomy.SetClock(clock, reset)
	owner.assembly.loginPasses.SetClock(clock, reset)
	call("/LoginUser", nil)
	assertClaims(12, 440000)
	claim(12)
	claim(440000)
	assertClaims(12, 440000)
	if got := freeDiamonds(); got != before+2100 {
		t.Fatalf("claim retries credited diamonds again: got %d, want %d", got, before+2100)
	}
	if got := assertCashMail(562, 100); got != freeMail {
		t.Fatal("free clear retry created another reward mail")
	}
	if got := assertCashMail(563, 2000); got != premiumMail {
		t.Fatal("premium clear retry created another reward mail")
	}
	call("/MailOpen", appScalar(2, freeMail))
	call("/MailOpen", appScalar(2, premiumMail))
	if got := freeDiamonds(); got != before+2100 {
		t.Fatalf("mail retries credited diamonds again: got %d, want %d", got, before+2100)
	}
}
