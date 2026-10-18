//go:build integration

package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func TestMonsterHuntRankingSeasonRewardPaysActualPlaceAfterRollover(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	original, e := factory.design.source.MonsterHunt(75)
	if e != nil {
		t.Fatal(e)
	}
	// Synthetic season tiers exercise distinct payouts through the real economy.
	design := *original
	design.Ranks = map[uint64][]gamedata.MonsterHuntRankReward{1: {{ID: 1, Type: 0, Ranking: 1, Rewards: []gamedata.BattleReward{{Type: 4, Count: 11}}}, {ID: 2, Type: 0, Ranking: 2, Rewards: []gamedata.BattleReward{{Type: 4, Count: 5}}}}}
	load := func(uint64) (*gamedata.MonsterHunt, error) { return &design, nil }
	presets, e := gamedata.LoadMonsterHuntPresetDesign(factory.options.gameData, factory.options.gameDataVersion)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	playing := monsterhunt.Season{ID: 81, Hunt: 75, Start: uint64(now.Add(-time.Hour).UnixMilli()), End: uint64(now.Add(time.Hour).UnixMilli()), Calculate: uint64(now.Add(2 * time.Hour).UnixMilli()), RankGroup: 1}
	factory.design.monsterHunt, e = monsterhunt.NewRules([]monsterhunt.Season{playing}, factory.seeds.defaults, presets, load)
	if e != nil {
		t.Fatal(e)
	}
	shared, e := openSharedRuntime(context.Background(), factory.options, factory.design)
	if e != nil {
		t.Fatal(e)
	}
	factory.shared, factory.pvpSource = shared, shared.pvp
	scenarios := []*monsterHuntScenario{{factory: factory, account: accounts[0]}, {factory: factory, account: accounts[1]}}
	for _, s := range scenarios {
		s.open(t)
	}
	t.Cleanup(func() {
		for _, s := range scenarios {
			if s.owner != nil {
				if e := s.owner.Close(); e != nil {
					t.Error(e)
				}
			}
		}
		if e := shared.store.Close(); e != nil {
			t.Error(e)
		}
	})
	hp, e := design.HP(3)
	if e != nil {
		t.Fatal(e)
	}
	scenarios[0].battle(t, "alice-clear", 3, true, 100, hp)
	scenarios[1].battle(t, "bob-clear", 3, true, 200, hp)
	for _, s := range scenarios {
		var publication monsterhunt.RankPublication
		s.command(t, func(ctx command.Context) ([]byte, error) {
			owner, e := s.owner.assembly.ProfileOwner(ctx)
			if e != nil {
				return nil, e
			}
			records, e := s.owner.assembly.monsterHuntService.RankRecords()
			publication = monsterhunt.RankPublication{Account: s.account, Version: 1, Profile: monsterhunt.RankProfile{Owner: owner, Name: s.account}, Records: records}
			return nil, e
		})
		if e := shared.huntRanks.Publish(context.Background(), publication); e != nil {
			t.Fatal(e)
		}
	}
	value := func(raw []byte, field int) uint64 { n, _, _ := wire.Varint(raw, field); return n }
	query := func(s *monsterHuntScenario, path string, seq uint64) []byte {
		return s.command(t, func(ctx command.Context) ([]byte, error) {
			_, out, _, e := s.owner.assembly.monsterHuntService.Handle(ctx, path, wire.AppendVarint(nil, 1, seq))
			return out, e
		})
	}
	for i, s := range scenarios {
		out := query(s, "/MonsterHuntUserInfo", 1)
		if value(out, 2) != uint64(2-i) {
			t.Fatal("user info fabricated first place instead of multiplayer rank")
		}
	}
	before := []uint64{scenarios[0].assets(t).Wallet.Gold, scenarios[1].assets(t).Wallet.Gold}
	// Closing one season and opening the next must retain the old pending reward.
	playing.Start, playing.End, playing.Calculate = uint64(now.Add(-3*time.Hour).UnixMilli()), uint64(now.Add(-2*time.Hour).UnixMilli()), uint64(now.Add(-time.Hour).UnixMilli())
	next := monsterhunt.Season{ID: 82, Hunt: 75, Start: uint64(now.Add(-time.Minute).UnixMilli()), End: uint64(now.Add(time.Hour).UnixMilli()), Calculate: uint64(now.Add(2 * time.Hour).UnixMilli()), RankGroup: 1}
	factory.design.monsterHunt, e = monsterhunt.NewRules([]monsterhunt.Season{playing, next}, factory.seeds.defaults, presets, load)
	if e != nil {
		t.Fatal(e)
	}
	for i, s := range scenarios {
		s.reopen(t)
		info := query(s, "/MonsterHuntUserInfo", 2)
		user, _, e := wire.Bytes(info, 1)
		if e != nil || value(user, 1) != 82 || value(user, 10) != 1 {
			t.Fatal("rollover lost pending season reward or replaced current season")
		}
		reward := query(s, "/MonsterHuntSeasonReward", 3)
		if value(reward, 1) != uint64(2-i) {
			t.Fatal("season payout used first place for both accounts")
		}
		want := uint64(5)
		if i == 1 {
			want = 11
		}
		if s.assets(t).Wallet.Gold != before[i]+want {
			t.Fatal("rank tier was not credited to the player's actual wallet")
		}
		replay := query(s, "/MonsterHuntSeasonReward", 3)
		if !bytes.Equal(reward, replay) {
			t.Fatal("season reward receipt changed on retry")
		}
		s.reopen(t)
		info = query(s, "/MonsterHuntUserInfo", 4)
		user, _, _ = wire.Bytes(info, 1)
		if value(user, 1) != 82 || value(user, 10) != 0 || s.assets(t).Wallet.Gold != before[i]+want {
			t.Fatal("reopen lost claim, duplicated currency, or contaminated new season")
		}
	}
}
