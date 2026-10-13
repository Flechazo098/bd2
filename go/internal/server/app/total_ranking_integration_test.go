//go:build integration

package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
)

// LocalText1300023 ranks highest cleared difficulty, then damage. TotalRankUI
// consumes the same identity-bearing hunt rows as MonsterHuntRankInfo.
func TestTotalRankingSharesDurableHuntResultsAcrossPlayers(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	design, err := factory.design.source.MonsterHunt(75)
	if err != nil {
		t.Fatal(err)
	}
	presets, err := gamedata.LoadMonsterHuntPresetDesign(factory.options.gameData, factory.options.gameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	factory.design.monsterHunt, err = monsterhunt.NewRules([]monsterhunt.Season{{ID: 81, Hunt: 75, Start: uint64(now.Add(-time.Hour).UnixMilli()), End: uint64(now.Add(time.Hour).UnixMilli())}}, factory.seeds.defaults, presets, factory.design.source.MonsterHunt)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	factory.shared, factory.pvpSource = shared, shared.pvp
	scenarios := []*monsterHuntScenario{{factory: factory, account: accounts[0]}, {factory: factory, account: accounts[1]}}
	for _, s := range scenarios {
		s.open(t)
	}
	t.Cleanup(func() {
		for _, s := range scenarios {
			if s.owner != nil {
				if err := s.owner.Close(); err != nil {
					t.Error(err)
				}
			}
		}
		if err := shared.store.Close(); err != nil {
			t.Error(err)
		}
	})
	var owners [2]uint64
	for i, s := range scenarios {
		s.command(t, func(c command.Context) ([]byte, error) {
			var err error
			owners[i], err = s.owner.assembly.ProfileOwner(c)
			return nil, err
		})
	}
	request := func(s *monsterHuntScenario, path, id string) []byte {
		t.Helper()
		body := wire.AppendVarint(nil, 1, 1)
		c := player.Command{Identity: command.Identity{AccountID: s.account, SessionID: "ranking-client", RequestID: id}, Digest: sha256.Sum256(append([]byte(path), body...)), Requests: []player.Request{{Path: path, Body: body}}}
		if path == "/LoginUser" {
			c.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
		}
		reply, err := s.owner.Execute(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if len(reply.Responses) != 1 {
			t.Fatal("ranking client received no response")
		}
		return reply.Responses[0].Body
	}
	publish := func(s *monsterHuntScenario, id string) {
		t.Helper()
		request(s, "/LoginUser", id)
		tx, err := s.owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		events, readErr := outbox.Read(tx, 128)
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, event := range events {
			if err := shared.Consume(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if err := shared.Consume(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertOrder := func(id string, first, second uint64) {
		t.Helper()
		body := request(scenarios[0], "/TotalRanking", id)
		rows := rankingRows(t, body, 2)
		if len(rows) != 2 {
			t.Fatalf("shared board has %d players, want2", len(rows))
		}
		for i, expected := range []uint64{first, second} {
			if owner, _, err := wire.Varint(rows[i], 1); err != nil || owner != expected {
				t.Fatalf("ranking order owner%d want%d: %v", owner, expected, err)
			}
			if name, found, err := wire.Bytes(rows[i], 2); err != nil || !found || len(name) == 0 {
				t.Fatal("ranking row cannot open identified player's profile")
			}
			if rank, _, _ := wire.Varint(rows[i], 7); rank != uint64(i+1) {
				t.Fatalf("rank%d for place%d", rank, i+1)
			}
		}
		if season, _, _ := wire.Varint(body, 7); season != 81 {
			t.Fatalf("hunt board uses season%d", season)
		}
		personal := request(scenarios[0], "/MonsterHuntRankInfo", id+"-personal")
		own := rankingRows(t, personal, 2)
		if len(own) != 1 {
			t.Fatal("personal hunt rank missing")
		}
		expectedRank := uint64(1)
		if first != owners[0] {
			expectedRank = 2
		}
		if rank, _, _ := wire.Varint(own[0], 7); rank != expectedRank {
			t.Fatalf("personal rank%d disagrees with shared place%d", rank, expectedRank)
		}
	}
	hp3, err := design.HP(3)
	if err != nil {
		t.Fatal(err)
	}
	hp5, err := design.HP(5)
	if err != nil {
		t.Fatal(err)
	}
	scenarios[0].battle(t, "first-clear", 3, true, 100, hp3)
	scenarios[1].battle(t, "second-clear", 3, true, 200, hp3)
	publish(scenarios[0], "first-publication")
	publish(scenarios[1], "second-publication")
	assertOrder("same-difficulty", owners[1], owners[0])
	scenarios[0].battle(t, "higher-failure", 5, false, 1000000, hp5)
	publish(scenarios[0], "failed-publication")
	assertOrder("failed-attempt", owners[1], owners[0])
	scenarios[0].battle(t, "higher-clear", 5, true, 1, hp5/2)
	publish(scenarios[0], "higher-publication")
	assertOrder("cleared-difficulty", owners[0], owners[1])
	for _, s := range scenarios {
		if err := s.owner.Close(); err != nil {
			t.Fatal(err)
		}
		s.owner = nil
	}
	if err := shared.store.Close(); err != nil {
		t.Fatal(err)
	}
	shared, err = openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	factory.shared, factory.pvpSource = shared, shared.pvp
	for _, s := range scenarios {
		s.open(t)
	}
	assertOrder("after-process-restart", owners[0], owners[1])
	rows := shared.huntRanks.Top(81, 2)
	if len(rows) != 2 || math.Abs(rows[1].TopPercent-100) > 0.0001 {
		t.Fatal("rank percentage denominator lost the second player")
	}
}

func rankingRows(t *testing.T, body []byte, field int) [][]byte {
	t.Helper()
	var rows [][]byte
	if err := wire.Walk(body, func(f wire.Field) error {
		if f.Number == field {
			if f.Type != 2 {
				return fmt.Errorf("ranking message wire type%d", f.Type)
			}
			rows = append(rows, f.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}
