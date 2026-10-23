//go:build integration

package app

import (
	"errors"
	"testing"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world"
)

// Current QuestTable3006 has one introduction: quest1 / Quest_Main_00.
// GameFieldManager.CoCheckGuildRaidIntroTimelineAndOpenUI completes this
// presentation with a local watched flag, without QuestAccept or QuestClear.
func TestGuildRaidIntroCheckpointRestartsAndCompletesWithoutQuestProgress(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	checkpoint := world.StoryCheckpoint{ID: "guild-intro-first", PackID: 3006, QuestID: 1, Phase: "accept", Timeline: "Quest_Main_00", MapID: 300603}
	if intro := factory.design.world.FieldPacks[3006].IntroQuest; intro == nil || intro.ID != 1 || intro.AcceptCinemaName != "Quest_Main_00" {
		t.Fatalf("current GameData field introduction is missing: %+v", intro)
	}
	for _, id := range []int{1001, 2001, 3001, 3003, 3004, 3005} {
		if intro := factory.design.world.FieldPacks[id].IntroQuest; intro != nil {
			t.Fatalf("ordinary field quest chain %d was misclassified as a standalone introduction: %+v", id, intro)
		}
	}
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.worldService.StoryCheckpoint(ctx, world.StoryCheckpointRequest{StoryCheckpoint: checkpoint, Operation: "begin"}); !errors.Is(err, world.ErrInvalidRequest) {
			t.Fatalf("guild introduction allowed before entering its pack: %v", err)
		}
	})
	s.protocol(t, "/PackInGameInfo", trapRequest(100, 3006))
	s.position(t, 3006, 300603)
	beforeGold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	for name, mutate := range map[string]func(*world.StoryCheckpoint){
		"quest":            func(c *world.StoryCheckpoint) { c.QuestID = 2 },
		"difficulty":       func(c *world.StoryCheckpoint) { c.Difficulty = 1 },
		"map":              func(c *world.StoryCheckpoint) { c.MapID = 51 },
		"timeline":         func(c *world.StoryCheckpoint) { c.Timeline = "Quest_Main_01" },
		"completion-phase": func(c *world.StoryCheckpoint) { c.Phase = "complete" },
		"object":           func(c *world.StoryCheckpoint) { c.ObjectID = 1 },
		"talk":             func(c *world.StoryCheckpoint) { c.SimpleTalkGroupID = 1 },
		"unknown-pack":     func(c *world.StoryCheckpoint) { c.PackID = 999999 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := checkpoint
			mutate(&candidate)
			s.run(t, false, func(ctx command.Context, p *playerAssembly) {
				_, err := p.worldService.StoryCheckpoint(ctx, world.StoryCheckpointRequest{StoryCheckpoint: candidate, Operation: "begin"})
				if !errors.Is(err, world.ErrInvalidRequest) {
					t.Fatalf("invalid guild introduction accepted: %v", err)
				}
			})
		})
	}
	if pending := storyCheckpointCall(t, s.owner, "begin", checkpoint).Checkpoint; pending == nil || *pending != checkpoint {
		t.Fatalf("authored guild introduction failed to begin: %+v", pending)
	}
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	if pending := storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint; pending == nil || *pending != checkpoint {
		t.Fatalf("unfinished guild introduction did not survive reconnect: %+v", pending)
	}
	// Native entry restarts the cinema with a fresh request identity. It must
	// reuse the existing checkpoint rather than conflict or skip playback.
	restarted := checkpoint
	restarted.ID = "guild-intro-reconnected"
	if pending := storyCheckpointCall(t, s.owner, "begin", restarted).Checkpoint; pending == nil || *pending != checkpoint {
		t.Fatalf("restarted introduction did not reuse its pending checkpoint: %+v", pending)
	}
	if pending := storyCheckpointCall(t, s.owner, "complete", world.StoryCheckpoint{ID: checkpoint.ID}).Checkpoint; pending != nil {
		t.Fatalf("cinema completion left the guild introduction pending: %+v", pending)
	}
	storyCheckpointCall(t, s.owner, "complete", world.StoryCheckpoint{ID: checkpoint.ID})
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	if pending := storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint; pending != nil {
		t.Fatalf("completed guild introduction returned after reconnect: %+v", pending)
	}
	if s.owner.assembly.progressState.QuestCleared(1, 3006, 0) || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold {
		t.Fatal("introduction completion advanced a quest or granted rewards")
	}
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		request := append(trapRequest(101, 1), appScalar(3, 3006)...)
		if _, _, _, err := p.worldService.Handle(ctx, "/QuestClear", request); !errors.Is(err, world.ErrInvalidRequest) {
			t.Fatalf("field introduction became a claimable story quest: %v", err)
		}
	})
}
