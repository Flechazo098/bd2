//go:build integration

package app

import (
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/progress"
)

func TestStoryArchiveRestoresPlayableChaptersAndSeparateStoryProgress(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		for quest := 1; quest <= 46; quest++ {
			if err := p.progressState.ClearQuest(ctx, quest, 1, 0); err != nil {
				t.Fatal(err)
			}
		}
		for _, quest := range []int{101, 102} {
			if err := p.progressState.ClearQuest(ctx, quest, 1, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.progressState.SelectQuest(ctx, 1, progress.QuestSelection{Difficulty: 2}); err != nil {
			t.Fatal(err)
		}
		var tickets []gamedata.BattleReward
		for _, chapter := range []int{2, 3} {
			rule := p.design.world.Story.Packs[chapter].Open
			if rule == nil || rule.TicketID == 0 {
				t.Fatal("chapter access prerequisite is unavailable")
			}
			tickets = append(tickets, gamedata.BattleReward{Type: 19, ID: rule.TicketID, Count: 1})
		}
		if _, err := p.ownedItems.GrantOnce(ctx, "archive-chapter-unlock", tickets); err != nil {
			t.Fatal(err)
		}
		if err := p.progressState.ClearQuest(ctx, 1, 2, 0); err != nil {
			t.Fatal(err)
		}
	})
	chapter3Available := false
	for _, row := range appRows(s.protocol(t, "/PackInfo", appScalar(1, uint64(s.commands+1))), 1) {
		if appValue(row, 1) == 3 {
			chapter3Available = true
			if appValue(row, 8) != 0 {
				t.Fatal("archive unlocked a chapter that has never been entered")
			}
		}
	}
	if !chapter3Available {
		t.Fatal("chapter 3 access ticket did not make it available")
	}
	s.protocol(t, "/PackInGameInfo", trapRequest(uint64(s.commands+1), 3))
	assertArchive := func() {
		t.Helper()
		gold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold
		packInfo := appRows(s.protocol(t, "/PackInfo", appScalar(1, uint64(s.commands+1))), 1)
		chapter2, chapter3 := false, false
		for _, row := range packInfo {
			if appValue(row, 1) == 2 {
				chapter2 = appValue(row, 8) == 1
			}
			if appValue(row, 1) == 3 {
				chapter3 = appValue(row, 8) == 1
			}
		}
		if !chapter2 || !chapter3 {
			t.Fatal("story menu locks the free chapter despite satisfied world access")
		}
		preview := s.protocol(t, "/PackPreviewInfo", trapRequest(uint64(s.commands+1), 1))
		got := map[uint64]uint64{}
		for _, row := range appRows(preview, 2) {
			got[appValue(row, 1)] = appValue(row, 2)
		}
		// Current pack1 QuestTitleTable separates main1..46, side101..112,
		// and side301..319. StoryListPanel filters replay using each title.
		if len(got) != 3 || got[1] != 46 || got[2] != 102 || got[3] != 0 {
			t.Fatalf("replay lost completed story or unlocked an unplayed side story: %v", got)
		}
		if s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != gold {
			t.Fatal("opening the story archive granted a gameplay reward")
		}
	}
	assertArchive()
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	assertArchive()
	other := &trapPlayerScenario{factory: factory, account: accounts[1]}
	other.owner = openNicknamePlayer(t, factory, other.account)
	defer closeNicknamePlayer(t, other.owner)
	for _, row := range appRows(other.protocol(t, "/PackPreviewInfo", trapRequest(1, 1)), 2) {
		if appValue(row, 2) != 0 {
			t.Fatal("a new account inherited completed story replay")
		}
	}
}
