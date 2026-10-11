//go:build integration

package app

import (
	chatpolicy "bd2server/internal/server/domain/chat"
	"bd2server/internal/server/domain/command"
	sharedstorage "bd2server/internal/server/storage/shared"
	"context"
	"testing"
	"time"
)

func TestNativeChatPreferencesBlocksReportsAndGuildBootstrapPersist(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	for _, account := range accounts {
		owner := openNicknamePlayer(t, factory, account)
		seedGuildQualification(t, owner)
		seedNicknameTickets(t, owner, 1)
		closeNicknamePlayer(t, owner)
	}
	var shared *sharedRuntime
	var registry *playerRegistry
	close := func() {
		if registry != nil {
			if err := registry.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			registry = nil
		}
		if shared != nil {
			if err := shared.store.Close(); err != nil {
				t.Fatal(err)
			}
			shared = nil
		}
	}
	open := func() {
		var err error
		shared, err = openSharedRuntime(context.Background(), factory.options, factory.design)
		if err != nil {
			t.Fatal(err)
		}
		factory.shared, factory.pvpSource = shared, shared.pvp
		registry = newPlayerRegistry(factory, time.Minute)
	}
	t.Cleanup(close)
	open()
	a, b := friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	drainFriendPublications(t, registry, shared, accounts[0])
	drainFriendPublications(t, registry, shared, accounts[1])
	for _, client := range []*friendHTTPClient{a, b} {
		for field := 10; field <= 12; field++ {
			if appValue(client.loginBody, field) != 1 {
				t.Fatal("new account chat setting should begin enabled")
			}
		}
	}
	a.send("/ChatSettingUpdate", appScalar(3, 1), 0, 0)
	a.send("/RoomChatBlockAdd", appScalar(2, b.owner), 522, 0)
	if err := shared.store.View(context.Background(), func(tx *sharedstorage.Tx) error {
		var policy chatpolicy.Policy
		found, err := chatJSON(tx, "chat.policy", accounts[0], &policy)
		if err != nil {
			return err
		}
		if !found || len(policy.Blocked) != 1 || policy.Blocked[0] != b.owner {
			t.Fatal("HTTP block acknowledgement preceded shared chat policy publication")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.seq--
	a.send("/RoomChatBlockAdd", appScalar(2, b.owner), 522, 0)
	info := a.send("/RoomChatReportAndBlockInfo", nil, 521, 0)
	blocks := appRows(info, 1)
	if len(blocks) != 1 || appValue(blocks[0], 1) != b.owner || nicknameField(t, blocks[0], 2) == "" || appValue(blocks[0], 3) == 0 {
		t.Fatal("block info did not use real target and timestamp")
	}
	var reportID uint64
	for id := range factory.design.chat.ReportIDs {
		reportID = id
		break
	}
	report := append(appScalar(2, b.owner), appScalar(3, reportID)...)
	report = append(report, appMessage(4, []byte("actual reported guild message"))...)
	report = append(report, appMessage(5, []byte("-"))...)
	report = append(report, appScalar(6, uint64(time.Now().UnixMilli()))...)
	a.send("/RoomChatReport", report, 524, 0)
	a.seq--
	a.send("/RoomChatReport", report, 524, 0)
	for count := uint64(1); count < factory.design.chat.ReportLimit; count++ {
		a.send("/RoomChatReport", report, 524, 0)
	}
	a.send("/RoomChatReport", report, 524, 9003)
	info = a.send("/RoomChatReportAndBlockInfo", nil, 521, 0)
	reports := appRows(info, 2)
	if len(reports) != 1 || appValue(reports[0], 1) != factory.design.chat.ReportLimit || appValue(reports[0], 2) <= uint64(time.Now().UnixMilli()) {
		t.Fatal("report retry or limit changed daily report accounting")
	}
	if len(appRows(b.send("/RoomChatReportAndBlockInfo", nil, 521, 0), 1)) != 0 {
		t.Fatal("block affected another player's private policy")
	}
	create := guildCreateCommand(t, factory, accounts[0], "chat-guild", "ChatGuild", a.seq+1)
	created := a.send("/GuildCreate", create.Requests[0].Body[len(appScalar(1, a.seq+1)):], 296, 0)
	rows := appRows(created, 1)
	base := appRows(rows[0], 1)
	guildID := appValue(base[0], 1)
	b.send("/GuildJoin", appScalar(2, guildID), 300, 0)
	b.send("/GuildInfo", nil, 297, 0)
	beforeGuild := b.send("/GuildInfo", nil, 297, 0)
	b.send("/GuildNoticeUpdate", appMessage(2, []byte("Unauthorized notice")), 382, 9003)
	if afterGuild := b.send("/GuildInfo", nil, 297, 0); string(beforeGuild) != string(afterGuild) {
		t.Fatal("permission rejection changed guild state")
	}
	a.send("/GuildNoticeUpdate", appMessage(2, []byte("Members can chat here")), 382, 0)
	notice := b.send("/GuildNoticeInfo", nil, 381, 0)
	if nicknameField(t, notice, 1) != "Members can chat here" {
		t.Fatal("permission rejection prevented subsequent valid guild action")
	}
	b.send("/UserNicknameChange", appMessage(2, []byte("GuildMemberB")), 74, 0)
	b.send("/UserPortraitChange", appScalar(2, factory.design.profileDesign.DefaultPortraitCostumeID), 75, 0)
	drainFriendPublications(t, registry, shared, accounts[1])
	updatedMembers := appRows(a.send("/GuildInfo", nil, 297, 0), 2)
	foundUpdated := false
	for _, member := range updatedMembers {
		if appValue(member, 2) == b.owner && nicknameField(t, member, 3) == "GuildMemberB" && appValue(member, 5) == factory.design.profileDesign.DefaultPortraitCostumeID {
			foundUpdated = true
		}
	}
	if !foundUpdated {
		t.Fatal("guild member row retained stale nickname or portrait after profile edit")
	}
	close()
	open()
	a, b = friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	if appValue(a.loginBody, 10) != 0 || appValue(a.loginBody, 11) != 1 || appValue(a.loginBody, 12) != 0 {
		t.Fatal("chat preferences did not survive store reopen")
	}
	for _, client := range []*friendHTTPClient{a, b} {
		member, base := appRows(client.loginBody, 6), appRows(client.loginBody, 7)
		if len(member) != 1 || len(base) != 1 || appValue(member[0], 1) != guildID || appValue(member[0], 2) != client.owner || appValue(base[0], 1) != guildID {
			t.Fatal("login did not bootstrap actual current guild membership")
		}
	}
	info = a.send("/RoomChatReportAndBlockInfo", nil, 521, 0)
	if len(appRows(info, 1)) != 1 || appValue(appRows(info, 2)[0], 1) != factory.design.chat.ReportLimit {
		t.Fatal("chat moderation state did not survive reopen")
	}
	a.send("/RoomChatBlockRemove", appScalar(2, b.owner), 523, 0)
	if len(appRows(a.send("/RoomChatReportAndBlockInfo", nil, 521, 0), 1)) != 0 {
		t.Fatal("unblock did not remove durable policy")
	}
	a.send("/ChatSettingUpdate", appScalar(2, 2), 0, 9003)
	b.send("/GuildLeave", nil, 298, 0)
	close()
	open()
	b = friendHTTPLogin(t, registry, accounts[1])
	if len(appRows(b.loginBody, 6)) != 0 || len(appRows(b.loginBody, 7)) != 0 {
		t.Fatal("departed guild remained in login bootstrap")
	}
}

func TestChatReportQuotaResetsAtNextServerDay(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner := openNicknamePlayer(t, factory, accounts[0])
	defer closeNicknamePlayer(t, owner)
	now := time.Date(2026, 10, 11, 23, 59, 59, 0, time.UTC)
	owner.assembly.chatService.Now = func() time.Time { return now }
	owner.assembly.chatService.Target = func(command.Context, uint64) (string, error) { return "ReportedPlayer", nil }
	identity, err := factory.profiles.GameIdentity(context.Background(), accounts[1])
	if err != nil {
		t.Fatal(err)
	}
	var reportID uint64
	for id := range factory.design.chat.ReportIDs {
		reportID = id
		break
	}
	call := func(path string, fields []byte) []byte {
		t.Helper()
		tx, err := owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx := command.Context{State: tx, Identity: command.Identity{AccountID: accounts[0]}, Emit: func(command.Event) error { return nil }}
		_, body, handled, err := owner.assembly.chatService.Handle(ctx, path, append(appScalar(1, 1), fields...))
		if err != nil || !handled {
			_ = tx.Rollback()
			t.Fatalf("%s %v handled%v", path, err, handled)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return body
	}
	report := append(appScalar(2, uint64(identity.OwnerIndex)), appScalar(3, reportID)...)
	report = append(report, appMessage(4, []byte("Reported text"))...)
	report = append(report, appMessage(5, []byte("-"))...)
	report = append(report, appScalar(6, uint64(now.UnixMilli()))...)
	call("/RoomChatReport", report)
	info := call("/RoomChatReportAndBlockInfo", nil)
	if appValue(appRows(info, 2)[0], 1) != 1 {
		t.Fatal("initial report missing")
	}
	now = now.Add(time.Second)
	info = call("/RoomChatReportAndBlockInfo", nil)
	if appValue(appRows(info, 2)[0], 1) != 0 {
		t.Fatal("daily report quota did not reset")
	}
	call("/RoomChatReport", report)
	info = call("/RoomChatReportAndBlockInfo", nil)
	if appValue(appRows(info, 2)[0], 1) != 1 {
		t.Fatal("new day report did not consume new day's quota")
	}
}
