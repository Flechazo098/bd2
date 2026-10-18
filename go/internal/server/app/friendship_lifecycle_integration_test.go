//go:build integration

package app

import (
	"bytes"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
)

// FriendshipPacket defaults an owned costume to level1 before any counseling.
// GameData501's first two stories award80 diamonds at levels1 and3; favorite
// gift7001 gives40EXP, and the first two level thresholds are50 and60.
func TestFriendshipStoriesFollowEarnedLevelsAndSurviveReentryWithoutDuplicateRewards(t *testing.T) {
	s := newFriendshipScenario(t)
	var before, giftIndex uint64
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if err := p.eventRegistry.Replace(nil); err != nil {
			t.Fatal(err)
		}
		if _, err := p.collection.GrantCostumes(ctx, "friendship-owned-costume", []uint64{501}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		items, err := p.ownedItems.GrantOnce(ctx, "friendship-gifts", []gamedata.BattleReward{{Type: 8, ID: 7001, Count: 3}})
		if err != nil || len(items) != 1 {
			t.Fatalf("supply gifts: %v", err)
		}
		giftIndex = items[0].InvenIndex
		before = p.wallet.Snapshot(ctx).FreeJewelry
	})
	first := friendshipEpisodeRequest(10, 501, 1)
	response := s.protocol(t, "/FriendshipSpecialEpisodeClear", first)
	assertFriendshipDiamonds(t, s, before+80)
	s.reopen(t)
	if replay := s.protocol(t, "/FriendshipSpecialEpisodeClear", first); !bytes.Equal(response, replay) {
		t.Fatal("reopening lost the acknowledged story completion")
	}
	s.protocol(t, "/FriendshipSpecialEpisodeClear", friendshipEpisodeRequest(11, 501, 1))
	assertFriendshipDiamonds(t, s, before+80)
	if _, err := s.execute("/FriendshipSpecialEpisodeClear", friendshipEpisodeRequest(12, 501, 2)); err == nil {
		t.Fatal("next story opened without earning its friendship level")
	}
	assertFriendshipEpisodes(t, s, 501, 1)
	request := wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(nil, 1, 13), 2, 501), 3, inventoryItem(giftIndex, 7001, 8, 3))
	s.protocol(t, "/FriendshipGift", request)
	s.reopen(t)
	s.protocol(t, "/FriendshipGift", request)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		if level := p.friendshipService.CurrentLevel(501); level != 3 {
			t.Fatalf("three favorite gifts should earn level3, got%d", level)
		}
		for _, item := range p.ownedItems.All(ctx) {
			if item.InvenIndex == giftIndex && item.Count != 0 {
				t.Fatal("consumed gifts reappeared after reopening")
			}
		}
	})
	s.protocol(t, "/FriendshipSpecialEpisodeClear", friendshipEpisodeRequest(14, 501, 2))
	s.reopen(t)
	assertFriendshipEpisodes(t, s, 501, 1, 2)
	assertFriendshipDiamonds(t, s, before+160)
}

// FriendshipStoryPopupUI_StoryItem waives the level requirement only for the
// scheduled group/episode, but still requires the preceding story to be clear.
func TestFriendshipEventUnlockIsEpisodeSpecificAndKeepsCompletedStoriesAfterTheEvent(t *testing.T) {
	s := newFriendshipScenario(t)
	var before, coinsBefore uint64
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.collection.GrantCostumes(ctx, "event-friendship-owned-costume", []uint64{60804}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if err := p.eventRegistry.Replace([]events.Schedule{{UID: 991, Type: 24, ID: 60804, SubID: 2, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}}); err != nil {
			t.Fatal(err)
		}
		before = p.wallet.Snapshot(ctx).FreeJewelry
		for _, item := range p.ownedItems.All(ctx) {
			if item.Type == 8 && item.ID == 2068 {
				coinsBefore += item.Count
			}
		}
	})
	if _, err := s.execute("/FriendshipSpecialEpisodeClear", friendshipEpisodeRequest(20, 60804, 2)); err == nil {
		t.Fatal("event bypassed the required preceding story")
	}
	s.run(t, false, func(_ command.Context, p *playerAssembly) {
		now := time.Now()
		if err := p.eventRegistry.Replace([]events.Schedule{{UID: 991, Type: 24, ID: 60804, SubID: 2, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}}); err != nil {
			t.Fatal(err)
		}
	})
	s.protocol(t, "/FriendshipSpecialEpisodeClear", friendshipEpisodeRequest(21, 60804, 1))
	s.protocol(t, "/FriendshipSpecialEpisodeClear", friendshipEpisodeRequest(22, 60804, 2))
	if _, err := s.execute("/FriendshipSpecialEpisodeClear", friendshipEpisodeRequest(23, 60804, 3)); err == nil {
		t.Fatal("one scheduled episode unlocked an unscheduled episode")
	}
	s.reopen(t)
	s.run(t, false, func(_ command.Context, p *playerAssembly) {
		if err := p.eventRegistry.Replace(nil); err != nil {
			t.Fatal(err)
		}
	})
	s.protocol(t, "/FriendshipSpecialEpisodeClear", friendshipEpisodeRequest(24, 60804, 2))
	assertFriendshipEpisodes(t, s, 60804, 1, 2)
	assertFriendshipDiamonds(t, s, before+160)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		var coins uint64
		for _, item := range p.ownedItems.All(ctx) {
			if item.Type == 8 && item.ID == 2068 {
				coins += item.Count
			}
		}
		if coins != coinsBefore+650 {
			t.Fatalf("only scheduled episode2 should grant event coins once: got%d want%d", coins, coinsBefore+650)
		}
	})
}

// FriendshipPacket consumes free AP before stacked AP. The per-costume daily
// limit is separate; an acknowledged consultation must not charge twice.
func TestFriendshipCounselingConsumesStackedAPOnlyAfterDailyFreeAPAndPersistsTheStory(t *testing.T) {
	s := newFriendshipScenario(t)
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.collection.GrantCostumes(ctx, "counseling-costumes", []uint64{501, 801, 1001, 1002}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		if _, err := p.eventEconomy.Apply(ctx, "stacked-counseling-supply", nil, []gamedata.Reward{{Type: 72, Count: 1}}); err != nil {
			t.Fatal(err)
		}
	})
	counsel := func(seq, costume uint64) []byte {
		return wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, costume), 3, 1)
	}
	for i, costume := range []uint64{501, 801, 1001} {
		s.protocol(t, "/FriendshipCounseling", counsel(uint64(30+i), costume))
	}
	s.reopen(t)
	s.run(t, false, func(_ command.Context, p *playerAssembly) {
		remaining, err := p.friendshipService.FriendshipAP()
		if err != nil || remaining != 0 {
			t.Fatalf("three consultations did not exhaust free AP: remaining%d err%v", remaining, err)
		}
	})
	request := counsel(33, 1002)
	s.protocol(t, "/FriendshipCounseling", request)
	s.reopen(t)
	s.protocol(t, "/FriendshipCounseling", request)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		balance, err := p.eventEconomy.CurrencyBalance(ctx, 72)
		if err != nil || balance != 0 {
			t.Fatalf("fourth consultation did not spend exactly one stacked AP: balance%d err%v", balance, err)
		}
		if level := p.friendshipService.CurrentLevel(1002); level != 2 {
			t.Fatalf("consultation progress was lost or applied twice: level%d", level)
		}
	})
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.eventEconomy.Apply(ctx, "stacked-counseling-second-supply", nil, []gamedata.Reward{{Type: 72, Count: 1}}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := s.execute("/FriendshipCounseling", counsel(34, 1002)); err == nil {
		t.Fatal("daily costume limit allowed a repeated consultation")
	}
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		balance, err := p.eventEconomy.CurrencyBalance(ctx, 72)
		if err != nil || balance != 1 {
			t.Fatalf("rejected repeated consultation consumed stacked AP: balance%d err%v", balance, err)
		}
	})
	info := s.protocol(t, "/FriendshipInfo", wire.AppendVarint(nil, 1, 35))
	var completed bool
	if err := wire.Walk(info, func(field wire.Field) error {
		if field.Number == 2 {
			id, _, err := wire.Varint(field.Value, 1)
			if err != nil {
				return err
			}
			if id == 1002 {
				completed = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("reentering friendship lost the completed consultation story")
	}
}

func newFriendshipScenario(t *testing.T) *trapPlayerScenario {
	t.Helper()
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	s := &trapPlayerScenario{factory: factory, owner: owner, account: accounts[0]}
	t.Cleanup(func() {
		if err := s.owner.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func friendshipEpisodeRequest(seq, group, id uint64) []byte {
	return wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, group), 3, id)
}

func assertFriendshipDiamonds(t *testing.T, s *trapPlayerScenario, want uint64) {
	t.Helper()
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		if got := p.wallet.Snapshot(ctx).FreeJewelry; got != want {
			t.Fatalf("friendship story rewards: diamonds got%d want%d", got, want)
		}
	})
}

func assertFriendshipEpisodes(t *testing.T, s *trapPlayerScenario, group uint64, want ...uint64) {
	t.Helper()
	response := s.protocol(t, "/FriendshipSpecialEpisodeInfo", wire.AppendVarint(nil, 1, uint64(100+s.commands)))
	completed := make(map[uint64]bool)
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number != 1 {
			return nil
		}
		gotGroup, _, err := wire.Varint(field.Value, 1)
		if err != nil {
			return err
		}
		id, _, err := wire.Varint(field.Value, 2)
		if err != nil {
			return err
		}
		if gotGroup != group || completed[id] {
			t.Fatalf("wrong or repeated story completion: group%d episode%d", gotGroup, id)
		}
		completed[id] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(completed) != len(want) {
		t.Fatalf("saved stories got%v want%v", completed, want)
	}
	for _, id := range want {
		if !completed[id] {
			t.Fatalf("completed story%d lost after reopening", id)
		}
	}
}
