//go:build integration

package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/gateway/transport"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func storyCheckpointCall(t *testing.T, owner *playerInstance, operation string, checkpoint world.StoryCheckpoint) world.StoryCheckpointReply {
	t.Helper()
	body, err := json.Marshal(world.StoryCheckpointRequest{StoryCheckpoint: checkpoint, Operation: operation})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := owner.Execute(context.Background(), player.Command{Identity: command.Identity{AccountID: owner.accountID, SessionID: "story-presentation", RequestID: fmt.Sprint(systemSequence.Add(1))}, Digest: sha256.Sum256(body), System: &player.SystemRequest{Kind: player.StoryCheckpoint, Body: body}})
	if err != nil || len(reply.Responses) != 1 {
		t.Fatalf("%s checkpoint: %v", operation, err)
	}
	var out world.StoryCheckpointReply
	if err := json.Unmarshal(reply.Responses[0].Body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// 2.36.7 GameFieldManager starts QuestTable6/1's accept cinema from map629.
// GameData 20261008155037 MapTable assigns629 to pack6, while PackTable6's
// field21 lists only601,606,608,615,630,631. It is not the full map catalog.
func TestStoryChapterEntryCheckpointAcceptsInteriorMapAndSurvivesReload(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if rule := p.design.world.Story.Packs[6].Open; rule != nil && rule.TicketID != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "chapter6-entry-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
	})
	s.protocol(t, "/PackInGameInfo", trapRequest(1, 6))
	s.position(t, 6, 629)
	beforeGold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	checkpoint := world.StoryCheckpoint{ID: "chapter6-entry", PackID: 6, QuestID: 1, Phase: "accept", Timeline: "Quest_Main_01_01", MapID: 629}
	foreignMap := checkpoint
	foreignMap.MapID = 51
	body, err := json.Marshal(world.StoryCheckpointRequest{StoryCheckpoint: foreignMap, Operation: "begin"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.owner.Execute(context.Background(), player.Command{Identity: command.Identity{AccountID: s.account, SessionID: "story-presentation", RequestID: fmt.Sprint(systemSequence.Add(1))}, Digest: sha256.Sum256(body), System: &player.SystemRequest{Kind: player.StoryCheckpoint, Body: body}})
	if !errors.Is(err, world.ErrInvalidRequest) {
		t.Fatalf("checkpoint in a different chapter's map was accepted: %v", err)
	}
	if pending := storyCheckpointCall(t, s.owner, "begin", checkpoint).Checkpoint; pending == nil || *pending != checkpoint {
		t.Fatalf("chapter entry cinema did not begin: %+v", pending)
	}
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	if pending := storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint; pending == nil || *pending != checkpoint {
		t.Fatalf("chapter entry cinema was lost after restart: %+v", pending)
	}
	storyCheckpointCall(t, s.owner, "complete", world.StoryCheckpoint{ID: checkpoint.ID})
	if pending := storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint; pending != nil {
		t.Fatalf("finished entry cinema remained pending: %+v", pending)
	}
	if s.owner.assembly.progressState.QuestCleared(1, 6, 0) || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold {
		t.Fatal("entry cinema recovery awarded a quest-clear reward or advanced the quest")
	}
}

// Native FieldResearchObjectController grants a research reward only when
// CommonPacket.IsClearResearchObject is false; PackInGameInfo restores that
// set from field11. GameData pack5/5501 in map55 grants830 gold and collection504.
func TestResearchPointRestoresCollectedStateWithoutRepeatingRewardDelta(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if rule := p.design.world.Story.Packs[5].Open; rule != nil && rule.TicketID != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "research-point-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
	})
	s.protocol(t, "/PackInGameInfo", trapRequest(1, 5))
	s.position(t, 5, 55)
	beforeGold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	response := s.protocol(t, "/FieldObjectResearch", wire.AppendVarint(trapRequest(2, 5), 3, 5501))
	collection := false
	for _, item := range appRows(response, 2) {
		collection = collection || appValue(item, 2) == 504 && appValue(item, 3) == 17 && appValue(item, 4) == 1
	}
	if !collection || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold+830 {
		t.Fatal("research point did not grant its gold and collection reward")
	}
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	entry := s.protocol(t, "/PackInGameInfo", trapRequest(3, 5))
	if id, found, err := wire.Varint(entry, 11); err != nil || !found || id != 5501 {
		t.Fatalf("rejoined field did not restore researched point: id%d found%v err%v", id, found, err)
	}
	preview := s.protocol(t, "/FieldObjectPreview", trapRequest(4, 5))
	if id, found, err := wire.Varint(preview, 2); err != nil || !found || id != 5501 {
		t.Fatalf("reward preview did not restore researched point: id%d found%v err%v", id, found, err)
	}
	response = s.protocol(t, "/FieldObjectResearch", wire.AppendVarint(trapRequest(5, 5), 3, 5501))
	if len(appRows(response, 2)) != 0 || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold+830 {
		t.Fatal("research retry after rejoin repeated a client reward delta or paid gold twice")
	}
}

func TestStoryCheckpointHTTPNativeSessionReachesActorAndPersistsCompletion(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() {
		if s.owner != nil {
			closeNicknamePlayer(t, s.owner)
		}
	})
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if rule := p.design.world.Story.Packs[5].Open; rule != nil && rule.TicketID != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "http-presentation-chapter-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		for quest := 1; quest < 26; quest++ {
			if err := p.progressState.ClearQuest(ctx, quest, 5, 0); err != nil {
				t.Fatal(err)
			}
		}
	})
	s.protocol(t, "/PackInGameInfo", trapRequest(1, 5))
	s.position(t, 5, 51)
	s.protocol(t, "/QuestClear", wire.AppendVarint(trapRequest(2, 26), 3, 5))
	settledGold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	closeNicknamePlayer(t, s.owner)
	s.owner = nil
	var registry *playerRegistry
	t.Cleanup(func() {
		if registry != nil {
			if err := registry.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	openHTTP := func() (http.Handler, string) {
		t.Helper()
		registry = newPlayerRegistry(factory, time.Minute)
		game, err := session.NewServer(registry, nicknameAuthenticator{accounts[0]})
		if err != nil {
			t.Fatal(err)
		}
		login, err := game.DispatchRaw(context.Background(), "/LoginUser", nicknameEncrypt(t, appScalar(1, 1), []byte("abcdefghijkrstuv024680wxyzlmnopq")), "")
		if err != nil {
			t.Fatal(err)
		}
		handler := transport.HTTP{StoryCheckpoints: storyCheckpoints{game: game, players: registry}}.Handler()
		return handler, "s=" + login.Cookie
	}
	callHTTP := func(handler http.Handler, cookie, operation string, checkpoint world.StoryCheckpoint, status int) world.StoryCheckpointReply {
		t.Helper()
		body, err := json.Marshal(world.StoryCheckpointRequest{StoryCheckpoint: checkpoint, Operation: operation})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/client/story-checkpoint", strings.NewReader(string(body)))
		request.Header.Set("Cookie", cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("HTTP checkpoint %s status=%d want%d: %s", operation, response.Code, status, response.Body.String())
		}
		var result world.StoryCheckpointReply
		if status == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	handler, cookie := openHTTP()
	callHTTP(handler, "", "read", world.StoryCheckpoint{}, http.StatusUnauthorized)
	pending := callHTTP(handler, cookie, "read", world.StoryCheckpoint{}, http.StatusOK).Checkpoint
	if pending == nil || pending.QuestID != 26 || pending.Timeline != "Quest_Main_26" || pending.MapID != 51 {
		t.Fatalf("HTTP actor path did not restore actual pending cinema: %+v", pending)
	}
	begin := *pending
	begin.ID = "http-client-replay"
	if adopted := callHTTP(handler, cookie, "begin", begin, http.StatusOK).Checkpoint; adopted == nil || adopted.ID != pending.ID {
		t.Fatal("HTTP actor path failed to adopt pending checkpoint")
	}
	if err := registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry = nil
	handler, freshCookie := openHTTP()
	callHTTP(handler, cookie, "read", world.StoryCheckpoint{}, http.StatusUnauthorized)
	if restored := callHTTP(handler, freshCookie, "read", world.StoryCheckpoint{}, http.StatusOK).Checkpoint; restored == nil || restored.ID != pending.ID {
		t.Fatal("HTTP checkpoint was lost across runtime/session restart")
	}
	if completed := callHTTP(handler, freshCookie, "complete", world.StoryCheckpoint{ID: pending.ID}, http.StatusOK).Checkpoint; completed != nil {
		t.Fatal("HTTP actor completion left playback pending")
	}
	if err := registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry = nil
	handler, lastCookie := openHTTP()
	if replay := callHTTP(handler, lastCookie, "read", world.StoryCheckpoint{}, http.StatusOK).Checkpoint; replay != nil {
		t.Fatal("HTTP completion did not persist across restart")
	}
	if err := registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry = nil
	owner := openNicknamePlayer(t, factory, accounts[0])
	defer closeNicknamePlayer(t, owner)
	if owner.assembly.wallet.Snapshot(command.Context{}).Gold != settledGold || !owner.assembly.progressState.QuestCleared(26, 5, 0) || owner.assembly.progressState.QuestCleared(27, 5, 0) {
		t.Fatal("HTTP playback recovery changed settled reward or quest progress")
	}
}

// Current GameData 20261008155037 QuestTable5/26 has CompleteCinemaName
// Quest_Main_26, TimelineStartMapId51, next27 and normal reward gold700.
// QuestPacket.RecvQuestClearResponse starts this cinema after server clear.
func TestUnfinishedQuestPresentationSurvivesReloadWithoutRewindingRewards(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if rule := p.design.world.Story.Packs[5].Open; rule != nil && rule.TicketID != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "presentation-chapter-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		for quest := 1; quest < 26; quest++ {
			if err := p.progressState.ClearQuest(ctx, quest, 5, 0); err != nil {
				t.Fatal(err)
			}
		}
	})
	s.protocol(t, "/PackInGameInfo", trapRequest(1, 5))
	s.position(t, 5, 51)
	assertHighestQuest := func(want uint64) {
		t.Helper()
		preview := s.protocol(t, "/PackPreviewInfo", trapRequest(uint64(s.commands+1), 5))
		story := appRows(preview, 2)
		if len(story) != 1 || appValue(story[0], 1) != 1 || appValue(story[0], 2) != want {
			t.Fatalf("story replay frontier did not follow the completed dialogue: %v, want %d", story, want)
		}
		rows := appRows(s.protocol(t, "/QuestMaxClearInfo", appScalar(1, uint64(s.commands+1))), 1)
		for _, row := range rows {
			if appValue(row, 1) == 5 {
				if got := appValue(row, 2); got != want {
					t.Fatalf("client mission unlock frontier=%d, want completed quest %d", got, want)
				}
				return
			}
		}
		t.Fatal("client cannot update its quest unlock frontier without a pack row")
	}
	assertHighestQuest(25)
	beforeGold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	clear := wire.AppendVarint(trapRequest(2, 26), 3, 5)
	s.protocol(t, "/QuestClear", clear)
	assertHighestQuest(26)
	if gold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold; gold != beforeGold+700 {
		t.Fatalf("quest gold=%d want%d", gold, beforeGold+700)
	}
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	assertHighestQuest(26)
	pending := storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint
	if pending == nil || pending.PackID != 5 || pending.QuestID != 26 || pending.Phase != "complete" || pending.Timeline != "Quest_Main_26" || pending.MapID != 51 {
		t.Fatalf("unfinished cinema was not restored: %+v", pending)
	}
	if !s.owner.assembly.progressState.QuestCleared(26, 5, 0) || s.owner.assembly.progressState.QuestCleared(27, 5, 0) {
		t.Fatal("presentation recovery rewound or advanced quest state")
	}
	retryBegin := *pending
	retryBegin.ID = "client-restarted-playback"
	if adopted := storyCheckpointCall(t, s.owner, "begin", retryBegin).Checkpoint; adopted == nil || adopted.ID != pending.ID {
		t.Fatal("restarted playback failed to adopt the committed presentation identity")
	}
	s.protocol(t, "/QuestClear", clear)
	if gold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold; gold != beforeGold+700 {
		t.Fatal("quest retry duplicated settled reward")
	}
	_, err := s.execute("/QuestClear", wire.AppendVarint(trapRequest(3, 27), 3, 5))
	if !errors.Is(err, world.ErrStoryCheckpointConflict) {
		t.Fatalf("next quest overwrote unfinished cinema: %v", err)
	}
	if err := s.owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.owner.assembly.progressState.QuestCleared(27, 5, 0) || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold+700 {
		t.Fatal("rejected presentation transition advanced progress or rewards")
	}
	if pendingAfter := storyCheckpointCall(t, s.owner, "complete", world.StoryCheckpoint{ID: "old-finished-dialogue"}).Checkpoint; pendingAfter == nil || pendingAfter.ID != pending.ID {
		t.Fatal("stale completion skipped the unfinished cinema")
	}
	if done := storyCheckpointCall(t, s.owner, "complete", world.StoryCheckpoint{ID: pending.ID}).Checkpoint; done != nil {
		t.Fatal("completed cinema stayed pending")
	}
	storyCheckpointCall(t, s.owner, "complete", world.StoryCheckpoint{ID: pending.ID})
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	if storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint != nil {
		t.Fatal("completed presentation returned after reload")
	}
	s.protocol(t, "/QuestClear", clear)
	if storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint != nil {
		t.Fatal("quest retry recreated a finished cinema")
	}
	if gold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold; gold != beforeGold+700 {
		t.Fatal("presentation acknowledgements changed settled gold")
	}
}

// Current pack5 FieldQuestObject111 lists quest11 and simple talk group671.
// Native InteractionQuest opens that balloon before sending QuestUpdate.
func TestUnfinishedStoryInteractionRestartsBeforeQuestUpdateAndClear(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if rule := p.design.world.Story.Packs[5].Open; rule != nil && rule.TicketID != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "interaction-chapter-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		for quest := 1; quest < 11; quest++ {
			if err := p.progressState.ClearQuest(ctx, quest, 5, 0); err != nil {
				t.Fatal(err)
			}
		}
	})
	s.protocol(t, "/PackInGameInfo", trapRequest(1, 5))
	s.position(t, 5, 52)
	beforeGold := s.owner.assembly.wallet.Snapshot(command.Context{}).Gold
	interaction := world.StoryCheckpoint{ID: "unfinished-dialogue-671", PackID: 5, QuestID: 11, Phase: "interaction", ObjectID: 111, MapID: 52, SimpleTalkGroupID: 671}
	storyCheckpointCall(t, s.owner, "begin", interaction)
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	pending := storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint
	if pending == nil || *pending != interaction {
		t.Fatalf("dialogue beginning was not recovered: %+v", pending)
	}
	if _, updated := s.owner.assembly.progressState.QuestInPack(11, 5, 0); updated || s.owner.assembly.progressState.QuestCleared(11, 5, 0) || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold {
		t.Fatal("unfinished dialogue advanced quest or granted reward")
	}
	storyCheckpointCall(t, s.owner, "complete", world.StoryCheckpoint{ID: interaction.ID})
	update := wire.AppendVarint(wire.AppendVarint(trapRequest(2, 11), 3, 5), 4, 1)
	s.protocol(t, "/QuestUpdate", update)
	if s.owner.assembly.progressState.QuestCleared(11, 5, 0) || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold {
		t.Fatal("dialogue update awarded quest-clear reward")
	}
	s.protocol(t, "/QuestClear", wire.AppendVarint(trapRequest(3, 11), 3, 5))
	completion := storyCheckpointCall(t, s.owner, "read", world.StoryCheckpoint{}).Checkpoint
	if completion == nil || completion.Phase != "complete" || completion.Timeline != "Quest_Main_11" || s.owner.assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold+700 {
		t.Fatal("finished interaction did not transition to settled quest and pending completion cinema")
	}
	if stillPending := storyCheckpointCall(t, s.owner, "complete", world.StoryCheckpoint{ID: interaction.ID}).Checkpoint; stillPending == nil || stillPending.ID != completion.ID {
		t.Fatal("delayed dialogue finish cleared the subsequent cinema")
	}
}
