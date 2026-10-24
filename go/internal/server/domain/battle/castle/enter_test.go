package castle

import (
	"testing"
	"time"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

// CommonPacket.EnterPack batches EvilCastleEnter(stage 0) before
// PackInGameInfo when entering the castle pack, so clearing the run must not
// require the pack entry; entering a floor still does.
func TestEvilCastleEnterClearsTheRunBeforeThePackIsEntered(t *testing.T) {
	d := testDesign()
	s := NewService(d)
	s.AttachClock(func() time.Time { return time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC) })
	s.AttachPeriod(func(command.Context) (uint64, bool, error) { return 1, true, nil })
	s.AttachCurrentPack(func(command.Context) (int, error) { return 3011, nil })
	ctx := command.Context{Identity: command.Identity{AccountID: "a", SessionID: "s"}, State: stateio.NewMemory()}
	execute(t, s, ctx, "/EvilCastleEnter", request(1, d.PackID, 0))
	if _, _, _, err := s.Handle(ctx, "/EvilCastleEnter", request(2, d.PackID, 1)); err == nil {
		t.Fatal("entering floor 1 outside the castle pack succeeded")
	}
}

type unrankedBoard struct{}

func (unrankedBoard) Top(uint64, uint64, int) []RankEntry { return nil }
func (unrankedBoard) Rank(owner uint64, record RankRecord) RankEntry {
	return RankEntry{RankRecord: record, Profile: RankProfile{Owner: owner, Name: "me"}, Rank: 1}
}

// EvilCastleRankUI.UpdateRanking dereferences my_ranking_info even when the
// player has no score, so the server always sends the player's own row, with
// their current portrait: a zero portrait shows the default costume.
func TestEvilCastleRankingAlwaysNamesThePlayer(t *testing.T) {
	d := testDesign()
	s := NewService(d)
	s.AttachClock(func() time.Time { return time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC) })
	s.AttachPeriod(func(command.Context) (uint64, bool, error) { return 1, true, nil })
	s.AttachRanking(7, unrankedBoard{})
	s.AttachRankProfile(func(command.Context) (RankProfile, error) {
		return RankProfile{Owner: 7, Name: "me", PortraitDesign: 42}, nil
	})
	ctx := command.Context{Identity: command.Identity{AccountID: "a", SessionID: "s"}, State: stateio.NewMemory()}
	mine, found, err := wire.Bytes(execute(t, s, ctx, "/EvilCastleRankingInfo", request(1, d.PackID, 1, 0)), 2)
	if err != nil || !found {
		t.Fatalf("ranking without a score has no my_ranking_info (%v)", err)
	}
	if owner, _, _ := wire.Varint(mine, 1); owner != 7 {
		t.Fatalf("my_ranking_info names owner %d, want 7", owner)
	}
	if portrait, _, _ := wire.Varint(mine, 5); portrait != 42 {
		t.Fatalf("my_ranking_info portrait design %d, want 42", portrait)
	}
	if rank, _, _ := wire.Varint(mine, 7); rank != 0 {
		t.Fatalf("an unscored player is ranked %d, want 0", rank)
	}
}

func TestPrideResumeKeepsUnsettledFloorThroughNativeLobbyEntry(t *testing.T) {
	d := testDesign()
	s := NewService(d)
	s.AttachClock(func() time.Time { return time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC) })
	s.AttachPeriod(func(command.Context) (uint64, bool, error) { return 1, true, nil })
	ctx := command.Context{Identity: command.Identity{AccountID: "a", SessionID: "relogin"}, State: stateio.NewMemory()}
	v := &state{Season: 1, Towers: map[string]tower{}, Floors: map[uint64]floor{1: {ID: 1, Point: 130}}, Run: &run{Floor: 1, Point: 130, Cleared: map[uint64]bool{51: true}}, Encounter: &encounter{ID: 1, Mode: 7, Monster: 52, Deck: 62, Season: 1, Receipt: "boss-battle"}}
	if err := save(ctx, v); err != nil {
		t.Fatal(err)
	}
	execute(t, s, ctx, "/EvilCastleEnter", request(1, d.PackID, 0))
	loaded, _, _, err := s.load(ctx)
	if err != nil || loaded.Run == nil || loaded.Encounter == nil || loaded.Run.Point != 130 || !loaded.Run.Cleared[51] {
		t.Fatal("native lobby entry discarded unfinished battle")
	}
	response := wire.AppendBytes(nil, 1, request(7, 62))
	response = wire.AppendVarint(response, 19, 0)
	response = wire.AppendVarint(response, 101, 1)
	response = wire.AppendBytes(response, 6, request(999, 999))
	out, err := s.ResumeBattle(ctx, "boss-battle", response)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := msgs(out, 6)
	if err != nil || len(monsters) != 1 || n(monsters[0], 1) != 52 || n(monsters[0], 2) != 62 || n(monsters[0], 6) != 1 || n(out, 7) != 130 || n(out, 19) != 1 || n(out, 101) != 1 {
		t.Fatalf("resume omitted floor context or retained stale monsters: %x", out)
	}
	if _, err = s.ResumeBattle(ctx, "another-battle", response); err == nil {
		t.Fatal("wrong encounter resumed")
	}
	if err = s.AbandonBattle(ctx); err != nil {
		t.Fatal(err)
	}
	execute(t, s, ctx, "/EvilCastleEnter", request(2, d.PackID, 0))
	loaded, _, _, err = s.load(ctx)
	if err != nil || loaded.Run != nil || loaded.Encounter != nil {
		t.Fatal("explicitly abandoned battle prevented lobby cleanup")
	}
}
