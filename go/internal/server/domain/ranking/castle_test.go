package ranking

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/battle/castle"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/shared"
	"bd2server/internal/server/storage/stateio"
	"context"
	"fmt"
	"testing"
	"time"
)

// Synthetic rewards distinguish rank one from rank two. Both players earn
// their scores through an ordinary encounter and boss before season rollover.
func TestCastlePlayedSeasonRanksAndRewardsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, e := shared.Open(directory)
	if e != nil {
		t.Fatal(e)
	}
	ranks, e := NewCastle(ctx, testPVPStore{store})
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := store.Close(); e != nil {
			t.Error(e)
		}
	}()
	season := uint64(1)
	settled := false
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	design := &gamedata.CastleRewardDesign{PackID: 31, Reset: gamedata.FieldResetSchedule{}, Floors: map[uint64]gamedata.CastleFloor{1: {ID: 1, Boss: 52, Monsters: map[uint64]uint64{51: 61, 52: 62}, Scoring: gamedata.CastleScoring{Base: 10, Turn: 100, BossMultiplier: 2}}}, Decks: map[uint64]gamedata.CastleDeck{61: {ID: 61}, 62: {ID: 62}}, Characters: map[uint64]gamedata.CastleCharacter{71: {Unique: 71}}, FloorRanks: []gamedata.CastleRankReward{{Rank: 1, Rewards: []gamedata.Reward{{Type: 4, Count: 3}}}, {Rank: 2, Rewards: []gamedata.Reward{{Type: 4, Count: 1}}}}, TotalRanks: []gamedata.CastleRankReward{{Rank: 1, Rewards: []gamedata.Reward{{Type: 4, Count: 8}}}, {Rank: 2, Rewards: []gamedata.Reward{{Type: 4, Count: 4}}}}}
	request := func(seq uint64, fields ...uint64) []byte {
		raw := wire.AppendVarint(nil, 1, seq)
		for i, value := range fields {
			raw = wire.AppendVarint(raw, i+2, value)
		}
		return raw
	}
	value := func(raw []byte, field int) uint64 { v, _, _ := wire.Varint(raw, field); return v }
	messages := func(raw []byte, field int) [][]byte {
		var rows [][]byte
		if e := wire.Walk(raw, func(f wire.Field) error {
			if f.Number == field {
				rows = append(rows, f.Value)
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		return rows
	}
	accounts := []command.Context{{Identity: command.Identity{AccountID: "alice", SessionID: "alice"}, State: stateio.NewMemory()}, {Identity: command.Identity{AccountID: "bob", SessionID: "bob"}, State: stateio.NewMemory()}}
	balance := func(c command.Context) uint64 {
		raw, _, e := c.State.LoadEntry("inventory", "test_wallet", "gold")
		if e != nil {
			t.Fatal(e)
		}
		return value(raw, 1)
	}
	proof := battle.SettlementEvidence{}
	open := func(owner uint64) *castle.Service {
		s := castle.NewService(design)
		s.AttachClock(func() time.Time { return now })
		s.AttachPeriod(func(command.Context) (uint64, bool, error) { return season, !settled, nil })
		s.AttachSeasonSettlement(func(id uint64) bool { return settled && id == 1 })
		s.AttachCurrentPack(func(command.Context) (int, error) { return 31, nil })
		s.AttachEvidence(func(command.Context) battle.SettlementEvidence { return proof })
		s.AttachTotalWarValidation(func(_ command.Context, ps []castle.Participant) error {
			for _, p := range ps {
				if p.CharacterIndex != 81 || p.CharacterID != 71 {
					return fmt.Errorf("not owned")
				}
			}
			return nil
		})
		s.AttachRanking(owner, ranks)
		s.AttachRewards(func(c command.Context, key string, rewards []gamedata.Reward) ([]byte, error) {
			if _, ok, e := c.State.LoadEntry("inventory", "test_grants", key); e != nil || ok {
				return nil, e
			}
			gold := balance(c)
			for _, reward := range rewards {
				if reward.Type != 4 {
					return nil, fmt.Errorf("unsupported test currency")
				}
				gold += reward.Count
			}
			if e := c.State.PutEntry("inventory", "test_wallet", "gold", wire.AppendVarint(nil, 1, gold)); e != nil {
				return nil, e
			}
			return nil, c.State.PutEntry("inventory", "test_grants", key, []byte("true"))
		})
		return s
	}
	execute := func(s *castle.Service, c command.Context, path string, body []byte) []byte {
		_, out, handled, e := s.Handle(c, path, body)
		if e != nil || !handled {
			t.Fatalf("%s handled=%v: %v", path, handled, e)
		}
		return out
	}
	services := []*castle.Service{open(11), open(22)}
	for i, s := range services {
		c := accounts[i]
		execute(s, c, "/EvilCastleEnter", request(1, 31, 1))
		blue := wire.AppendVarint(nil, 2, 81)
		blue = wire.AppendVarint(blue, 3, 71)
		hero := wire.AppendVarint(nil, 1, 81)
		hero = wire.AppendVarint(hero, 2, 71)
		hero = wire.AppendVarint(hero, 3, 1)
		for _, monster := range []uint64{51, 52} {
			deck := design.Floors[1].Monsters[monster]
			receipt := fmt.Sprintf("%s:%d", c.AccountID, monster)
			if _, e := s.EnterBattle(c, request(monster, 0, monster, deck, 7), receipt); e != nil {
				t.Fatal(e)
			}
			proof = battle.SettlementEvidence{Receipt: receipt, Mode: 7, Deck: deck, Monster: monster, Round: uint64(5 - i*4), InitialBlue: [][]byte{blue}}
			if _, e := s.CompleteBattle(c, wire.AppendBytes(request(monster+100, 1), 3, hero), receipt); e != nil {
				t.Fatal(e)
			}
		}
		records, e := s.RankRecords(c)
		if e != nil {
			t.Fatal(e)
		}
		if e = ranks.Publish(ctx, castle.RankPublication{Account: c.AccountID, Version: 1, Profile: castle.RankProfile{Owner: uint64(11 + i*11), Name: c.AccountID}, Records: records}); e != nil {
			t.Fatal(e)
		}
	}
	// Bob clears in one turn; Alice needs five, so Bob wins each floor and total.
	for i, s := range services {
		out := execute(s, accounts[i], "/EvilCastleRankingInfo", request(201, 31, 1, 1))
		rows := messages(out, 1)
		if len(rows) != 2 || value(rows[0], 1) != 22 || value(rows[1], 1) != 11 {
			t.Fatal("played cross-account ranking order is wrong")
		}
		own := messages(out, 2)
		if len(own) != 1 || value(own[0], 7) != uint64(2-i) {
			t.Fatal("personal ranking differs from board")
		}
		stage := execute(s, accounts[i], "/EvilCastleStageRankingInfo", request(202, 31, 1, 1))
		if value(stage, 1) != uint64(2-i) || value(stage, 3) != uint64(2-i) {
			t.Fatal("stage and total rank disagree")
		}
	}
	execute(services[0], accounts[0], "/EvilCastleRewardInfo", request(203, 31))
	if balance(accounts[0]) != 0 {
		t.Fatal("unsettled season paid rewards")
	}
	season = 2
	settled = true
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	store, e = shared.Open(directory)
	if e != nil {
		t.Fatal(e)
	}
	ranks, e = NewCastle(ctx, testPVPStore{store})
	if e != nil {
		t.Fatal(e)
	}
	for i, c := range accounts {
		s := open(uint64(11 + i*11))
		execute(s, c, "/EvilCastleInfo", request(204, 31))
		history := execute(s, c, "/EvilCastleStageRankingInfo", request(205, 31, 1, 1))
		if value(history, 1) != uint64(2-i) {
			t.Fatal("season change or shared restart lost historical rank")
		}
		claim := request(206, 31)
		out := execute(s, c, "/EvilCastleRewardInfo", claim)
		floorRows, totalRows := messages(out, 1), messages(out, 2)
		if len(floorRows) != 1 || len(totalRows) != 1 || value(floorRows[0], 1) != uint64(2-i) || value(totalRows[0], 1) != uint64(2-i) {
			t.Fatal("season payout used wrong player's rank")
		}
		want := uint64(5)
		if i == 1 {
			want = 11
		}
		if balance(c) != want {
			t.Fatalf("player %s credited %d, want %d", c.AccountID, balance(c), want)
		}
		execute(s, c, "/EvilCastleRewardInfo", claim)
		s = open(uint64(11 + i*11))
		execute(s, c, "/EvilCastleRewardInfo", request(207, 31))
		if balance(c) != want {
			t.Fatal("retry or reopen duplicated season payout")
		}
		info := execute(s, c, "/EvilCastleInfo", request(208, 31))
		if len(messages(info, 1)) != 0 {
			t.Fatal("old score leaked into next season")
		}
	}
}
