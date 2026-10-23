//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"math"
	"reflect"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
)

func TestMonsterHuntClearedDifficultyRankingAndRewardlessReplay(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	presets, err := gamedata.LoadMonsterHuntPresetDesign(factory.options.gameData, factory.options.gameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	design, err := factory.design.source.MonsterHunt(75)
	if err != nil {
		t.Fatal(err)
	}
	hp3, err := design.HP(3)
	if err != nil {
		t.Fatal(err)
	}
	hp5, err := design.HP(5)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	factory.design.monsterHunt, err = monsterhunt.NewRules([]monsterhunt.Season{{ID: 81, Hunt: 75, Start: uint64(now.Add(-time.Hour).UnixMilli()), End: uint64(now.Add(24 * time.Hour).UnixMilli())}}, factory.seeds.defaults, presets, factory.design.source.MonsterHunt)
	if err != nil {
		t.Fatal(err)
	}
	s := &monsterHuntScenario{factory: factory, account: accounts[0]}
	s.open(t)
	t.Cleanup(func() {
		if s.owner != nil {
			if err := s.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})

	s.battle(t, "initial-failure", 5, false, 9000, hp5)
	s.assertRank(t, 0, 0)
	if got := s.progress(t).StartHP; got != hp5/2 {
		t.Fatalf("failed challenge saved %d HP, want %d", got, hp5/2)
	}
	s.reopen(t)
	s.assertRank(t, 0, 0)
	if got := s.progress(t).StartHP; got != hp5/2 {
		t.Fatalf("reopening lost partial challenge HP: got %d, want %d", got, hp5/2)
	}
	s.battle(t, "resume-five", 5, false, 8000, hp5/2)
	s.assertRank(t, 0, 0)

	beforeClear := s.assets(t)
	first := s.battle(t, "clear-three", 3, true, 1200, hp3)
	if bundle, found, err := wire.Bytes(first, 7); err != nil || !found || len(bundle) == 0 {
		t.Fatal("first clear did not return its clear rewards")
	}
	s.assertClearRewards(t, beforeClear, 1, 3)
	s.assertRank(t, 3, 1200)
	s.battle(t, "higher-failure", 5, false, 900000, hp5)
	s.assertRank(t, 3, 1200)
	s.reopen(t)
	s.assertRank(t, 3, 1200)
	progress := s.progress(t)
	assets := s.assets(t)
	s.assertNoReplayRewards(t, s.battle(t, "improve-three", 3, true, 2000, hp3))
	s.assertRank(t, 3, 2000)
	s.assertProgress(t, progress)
	s.assertAssets(t, assets)
	s.assertNoReplayRewards(t, s.battle(t, "failed-three", 3, false, 500000, hp3))
	s.assertRank(t, 3, 2000)
	s.assertProgress(t, progress)
	s.assertAssets(t, assets)

	maximum := s.battle(t, "clear-five", 5, true, 1000, hp5/2)
	if bundle, found, err := wire.Bytes(maximum, 7); err != nil || !found || len(bundle) == 0 {
		t.Fatal("new maximum clear did not return its clear rewards")
	}
	s.assertRank(t, 5, 1000)
	s.assertClearRewards(t, assets, 4, 5)
	progress = s.progress(t)
	if progress.Level != 5 || progress.Level > design.MaxLevel {
		t.Fatalf("maximum clear hid the client's challenge entry: %+v", progress)
	}
	assets = s.assets(t)
	s.reopen(t)
	s.assertRank(t, 5, 1000)
	s.assertNoReplayRewards(t, s.battle(t, "failed-five-replay", 5, false, 500000, hp5))
	s.assertRank(t, 5, 1000)
	s.assertProgress(t, progress)
	s.assertAssets(t, assets)
	replay := s.battle(t, "improve-five", 5, true, 2000, hp5)
	s.assertNoReplayRewards(t, replay)
	s.assertRank(t, 5, 2000)
	s.assertProgress(t, progress)
	s.assertAssets(t, assets)
	s.reopen(t)
	s.assertRank(t, 5, 2000)
	retried := s.complete(t, "improve-five", 0, 2000)
	if !bytes.Equal(retried, replay) {
		t.Fatal("persisted battle retry changed its response")
	}
	s.assertAssets(t, assets)
	s.assertNoReplayRewards(t, s.battle(t, "worse-five", 5, true, 500, hp5))
	s.assertRank(t, 5, 2000)
	s.assertProgress(t, progress)
	s.assertAssets(t, assets)
}

type monsterHuntScenario struct {
	factory *PlayerFactory
	owner   *playerInstance
	account string
}

type monsterHuntProgress struct{ Level, StartHP, HighestHP uint64 }

type monsterHuntAssets struct {
	Wallet assets.Currency
	Items  map[[2]uint64]uint64
}

func (s *monsterHuntScenario) open(t *testing.T) {
	t.Helper()
	var err error
	s.owner, err = s.factory.open(s.account)
	if err != nil {
		t.Fatal(err)
	}
}

func (s *monsterHuntScenario) reopen(t *testing.T) {
	t.Helper()
	if err := s.owner.Close(); err != nil {
		t.Fatal(err)
	}
	s.owner = nil
	s.open(t)
}

func (s *monsterHuntScenario) command(t *testing.T, action func(command.Context) ([]byte, error)) []byte {
	t.Helper()
	tx, err := s.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	response, err := action(command.Context{Identity: command.Identity{AccountID: s.account, SessionID: "hunt-client"}, State: tx, InstanceIDs: s.factory.instanceIDs()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return response
}

func (s *monsterHuntScenario) battle(t *testing.T, receipt string, level uint64, won bool, damage, expectedHP uint64) []byte {
	t.Helper()
	design, err := s.factory.design.source.MonsterHunt(75)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 4, design.DeckID)
	request = wire.AppendVarint(request, 5, 8)
	request = wire.AppendVarint(request, 6, 75)
	request = wire.AppendVarint(request, 10, level)
	entry := s.command(t, func(ctx command.Context) ([]byte, error) {
		return s.owner.assembly.monsterHuntService.EnterBattle(ctx, request, receipt)
	})
	user, found, err := wire.Bytes(entry, 5)
	if err != nil || !found {
		t.Fatal("battle entry is missing the monster hunt user")
	}
	enteredLevel, _, err := wire.Varint(user, 3)
	if err != nil || enteredLevel != level {
		t.Fatalf("battle entry selected difficulty %d, want %d", enteredLevel, level)
	}
	hp, _, err := wire.Varint(user, 4)
	if err != nil || hp != expectedHP || hp <= 1 {
		t.Fatalf("battle entry returned %d HP, want %d", hp, expectedHP)
	}
	maxHP, _, err := wire.Varint(user, 6)
	if err != nil {
		t.Fatal(err)
	}
	expectedMax, err := design.HP(level)
	if err != nil || maxHP != expectedMax {
		t.Fatalf("selected difficulty maximum HP %d, want design HP %d: %v", maxHP, expectedMax, err)
	}
	var remaining uint64
	if !won {
		remaining = hp / 2
	}
	return s.complete(t, receipt, remaining, damage)
}

func (s *monsterHuntScenario) complete(t *testing.T, receipt string, remaining, damage uint64) []byte {
	t.Helper()
	request := wire.AppendVarint(nil, 7, remaining)
	attack := wire.AppendVarint(nil, 1, 1)
	attack = wire.AppendVarint(attack, 2, damage)
	request = wire.AppendBytes(request, 10, wire.AppendBytes(nil, 1, attack))
	return s.command(t, func(ctx command.Context) ([]byte, error) {
		return s.owner.assembly.monsterHuntService.CompleteBattle(ctx, request, receipt)
	})
}

func (s *monsterHuntScenario) assertRank(t *testing.T, difficulty, damage uint64) {
	t.Helper()
	response := s.command(t, func(ctx command.Context) ([]byte, error) {
		request := wire.AppendVarint(nil, 1, 1)
		request = wire.AppendVarint(request, 2, 81)
		_, response, handled, err := s.owner.assembly.monsterHuntService.Handle(ctx, "/MonsterHuntRankInfo", request)
		if !handled {
			return nil, fmt.Errorf("ranking request was not handled")
		}
		return response, err
	})
	rank, found, err := wire.Bytes(response, 2)
	if err != nil || !found {
		t.Fatal("missing personal ranking")
	}
	var score uint64
	if err := wire.Walk(rank, func(field wire.Field) error {
		if field.Number == 8 && field.Type == 1 {
			score = uint64(math.Float64frombits(binary.LittleEndian.Uint64(field.Value)))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if score/100000000000000 != difficulty || score%100000000000000 != damage {
		t.Fatalf("client ranking difficulty=%d damage=%d, want difficulty=%d damage=%d", score/100000000000000, score%100000000000000, difficulty, damage)
	}
}

func (s *monsterHuntScenario) progress(t *testing.T) monsterHuntProgress {
	t.Helper()
	response := s.command(t, func(ctx command.Context) ([]byte, error) {
		_, response, handled, err := s.owner.assembly.monsterHuntService.Handle(ctx, "/MonsterHuntUserInfo", wire.AppendVarint(nil, 1, 1))
		if !handled {
			return nil, fmt.Errorf("hunt user request was not handled")
		}
		return response, err
	})
	user, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatal("missing hunt user info")
	}
	var progress monsterHuntProgress
	for field, value := range map[int]*uint64{3: &progress.Level, 4: &progress.StartHP, 6: &progress.HighestHP} {
		*value, _, err = wire.Varint(user, field)
		if err != nil {
			t.Fatal(err)
		}
	}
	return progress
}

func (s *monsterHuntScenario) assertProgress(t *testing.T, expected monsterHuntProgress) {
	t.Helper()
	if got := s.progress(t); got != expected {
		t.Fatalf("replay changed main hunt progress: got %+v, want %+v", got, expected)
	}
}

func (s *monsterHuntScenario) assets(t *testing.T) monsterHuntAssets {
	t.Helper()
	result := monsterHuntAssets{Items: map[[2]uint64]uint64{}}
	s.command(t, func(ctx command.Context) ([]byte, error) {
		result.Wallet = s.owner.assembly.wallet.Snapshot(ctx)
		for _, item := range s.owner.assembly.ownedItems.All(ctx) {
			result.Items[[2]uint64{item.Type, item.ID}] += item.Count
		}
		return nil, nil
	})
	return result
}

func (s *monsterHuntScenario) assertAssets(t *testing.T, expected monsterHuntAssets) {
	t.Helper()
	if got := s.assets(t); !reflect.DeepEqual(got, expected) {
		t.Fatalf("owned rewards differ: got %+v, want %+v", got, expected)
	}
}

func (s *monsterHuntScenario) assertClearRewards(t *testing.T, before monsterHuntAssets, first, last uint64) {
	t.Helper()
	design, err := s.factory.design.source.MonsterHunt(75)
	if err != nil {
		t.Fatal(err)
	}
	expected := monsterHuntAssets{Wallet: before.Wallet, Items: maps.Clone(before.Items)}
	for level := first; level <= last; level++ {
		for _, reward := range design.Rewards[level].Clear {
			switch reward.Type {
			case 4:
				expected.Wallet.Gold += reward.Count
			case 8:
				expected.Items[[2]uint64{reward.Type, reward.ID}] += reward.Count
			default:
				t.Fatalf("current hunt clear reward type %d needs an owned-asset assertion", reward.Type)
			}
		}
	}
	s.assertAssets(t, expected)
}

func (s *monsterHuntScenario) assertNoReplayRewards(t *testing.T, response []byte) {
	t.Helper()
	for _, field := range []int{7, 8} {
		if _, found, err := wire.Bytes(response, field); err != nil || found {
			t.Fatalf("replay returned reward bundle field %d", field)
		}
	}
}
