package monsterhunt

import (
	"bd2server/internal/server/calendar"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func installed(t *testing.T) (*Service, *stateio.Memory) {
	t.Helper()
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	seed, e := readonly.Load(filepath.Join("..", "..", "..", "seed", "v2_35_10", "readonly.json"))
	if e != nil {
		t.Fatal(e)
	}
	calendars, e := calendar.LoadDirectory(filepath.Join("..", "..", "..", "..", "schedules"), "2.35.10", "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	seed, e = calendars.ApplyReadonly(seed)
	if e != nil {
		t.Fatal(e)
	}
	store := stateio.NewMemory()
	inv, e := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if e != nil {
		t.Fatal(e)
	}
	wallet, e := player.OpenWallet(store, player.Currency{Gold: 100000})
	if e != nil {
		t.Fatal(e)
	}
	s, e := Open(store, root, "20260923193640", seed, inv, wallet)
	if e != nil {
		t.Fatal(e)
	}
	c := s.current()
	s.now = func() time.Time { return time.UnixMilli(int64(c.Start + 1000)) }
	return s, store
}
func battleRequest(id, deck, mode uint64) []byte {
	b := wire.AppendVarint(nil, 1, 1)
	b = wire.AppendVarint(b, 4, deck)
	b = wire.AppendVarint(b, 5, mode)
	b = wire.AppendVarint(b, 6, id)
	return wire.AppendVarint(b, 10, 1)
}
func TestBattleSessionProgressRewardsAndPersistentRetry(t *testing.T) {
	s, store := installed(t)
	c := s.current()
	d, e := s.load(c.Hunt)
	if e != nil {
		t.Fatal(e)
	}
	req := battleRequest(c.Hunt, d.DeckID, BattleMode)
	if _, e = s.EnterBattle(req, "session:1"); e != nil {
		t.Fatal(e)
	}
	s.BeginSession("session")
	end := wire.AppendVarint(nil, 1, 2)
	end = wire.AppendVarint(end, 2, 1)
	out, e := s.CompleteBattle(end, "session:1")
	if e != nil {
		t.Fatal(e)
	}
	if _, ok, _ := wire.Bytes(out, 7); !ok {
		t.Fatal("clear reward missing")
	}
	u := s.state.Users[strconv.FormatUint(c.ID, 10)]
	if u.ClearLevel != 1 || u.Level != 2 || !u.Played {
		t.Fatalf("progress=%+v", u)
	}
	retry, e := s.CompleteBattle(end, "session:1")
	if e != nil || !bytes.Equal(out, retry) {
		t.Fatalf("retry=%x err=%v", retry, e)
	}
	bad := wire.AppendVarint(end, 7, 1)
	if _, e = s.CompleteBattle(bad, "session:1"); e == nil {
		t.Fatal("changed retry accepted")
	}
	restored, e := Open(store, s.root, s.version, s.seed, s.inventory, s.wallet)
	if e != nil {
		t.Fatal(e)
	}
	retry, e = restored.CompleteBattle(end, "session:1")
	if e != nil || !bytes.Equal(out, retry) {
		t.Fatalf("restored retry failed %v", e)
	}
}
func TestPracticeHasNoRewardsAndRejectsInvalidHP(t *testing.T) {
	s, _ := installed(t)
	c := s.current()
	d, e := s.load(c.Hunt)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnterBattle(battleRequest(c.Hunt, d.DeckID, PracticeMode), "p:1"); e != nil {
		t.Fatal(e)
	}
	hp, _ := d.HP(1)
	bad := wire.AppendVarint(nil, 7, hp+1)
	if _, e = s.CompleteBattle(bad, "p:1"); e == nil {
		t.Fatal("impossible HP accepted")
	}
	if b, e := s.CompleteBattle(nil, "p:1"); e != nil || len(b) != 0 {
		t.Fatalf("practice=%x err=%v", b, e)
	}
	if len(s.state.Users) != 0 {
		t.Fatal("practice changed competitive progress")
	}
}
func TestPresetSlotCurrencyAndIdempotency(t *testing.T) {
	s, _ := installed(t)
	req := wire.AppendVarint(nil, 1, 41)
	req = wire.AppendVarint(req, 2, 1)
	before := s.state.Slots
	for i := 0; i < 2; i++ {
		if _, _, _, e := s.HandleSession("/MonsterHuntPresetSlotAdd", req, "session"); e != nil {
			t.Fatal(e)
		}
	}
	if s.state.Slots != before+1 {
		t.Fatal("slot retry doubled purchase")
	}
	changed := wire.AppendVarint(nil, 1, 41)
	changed = wire.AppendVarint(changed, 2, 2)
	if _, _, _, e := s.HandleSession("/MonsterHuntPresetSlotAdd", changed, "session"); e == nil {
		t.Fatal("changed slot retry accepted")
	}
}
func TestDeckShapeRejectsRepeatedPositionAndAcceptsTeamThree(t *testing.T) {
	s := &Service{}
	char := wire.AppendVarint(nil, 1, 1)
	char = wire.AppendVarint(char, 3, 1)
	deck := wire.AppendVarint(nil, 1, 3)
	deck = wire.AppendBytes(deck, 2, char)
	if e := s.validateDecks([][]byte{deck}); e != nil {
		t.Fatal(e)
	}
	dup := wire.AppendVarint(nil, 1, 2)
	dup = wire.AppendVarint(dup, 3, 2)
	deck = wire.AppendBytes(deck, 2, dup)
	if e := s.validateDecks([][]byte{deck}); e == nil {
		t.Fatal("duplicate position accepted")
	}
}
func TestRankRewardUsesActualLocalPercentThreshold(t *testing.T) {
	s := &Service{}
	d := &gamedata.MonsterHunt{Ranks: map[uint64][]gamedata.MonsterHuntRankReward{1: {{Type: 1, Ranking: 10, Rewards: []gamedata.BattleReward{{Count: 10}}}, {Type: 1, Ranking: 100, Rewards: []gamedata.BattleReward{{Count: 1}}}}}}
	got := s.rankRewards(d, 1)
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("rewards=%+v", got)
	}
}

func TestDailyRewardUpgradePaysDifference(t *testing.T) {
	d := &gamedata.MonsterHunt{Rewards: map[uint64]gamedata.MonsterHuntRewards{1: {Daily: []gamedata.BattleReward{{Type: 4, Count: 100}}}, 2: {Daily: []gamedata.BattleReward{{Type: 4, Count: 150}, {Type: 8, ID: 1, Count: 2}}}}}
	if got := dailyDifference(d, 1, 1); len(got) != 0 {
		t.Fatalf("same level repeated payout=%v", got)
	}
	got := dailyDifference(d, 1, 2)
	if len(got) != 2 || got[0].Count != 50 || got[1].Count != 2 {
		t.Fatalf("upgrade=%v", got)
	}
}
func TestSessionRetryRejectsChangedBodyWithoutGameData(t *testing.T) {
	s := &Service{state: snapshot{Replies: map[string]reply{"sid:/MonsterHuntDeckSave:2": {Request: wire.AppendVarint(nil, 1, 2), Response: []byte{}}}}}
	req := wire.AppendVarint(nil, 1, 2)
	if _, _, ok, e := s.HandleSession("/MonsterHuntDeckSave", req, "sid"); e != nil || !ok {
		t.Fatal(e)
	}
	changed := wire.AppendVarint(req, 4, 1)
	if _, _, _, e := s.HandleSession("/MonsterHuntDeckSave", changed, "sid"); e == nil {
		t.Fatal("changed same-sequence request accepted")
	}
}
func TestRequestMalformedAndUnsignedWrapRejected(t *testing.T) {
	s := &Service{}
	for _, req := range [][]byte{{0xff}, wire.AppendVarint(nil, 1, ^uint64(0))} {
		if _, _, ok, e := s.HandleSession("/MonsterHuntUserInfo", req, "sid"); e == nil || !ok {
			t.Fatalf("bad request accepted %x", req)
		}
	}
}
