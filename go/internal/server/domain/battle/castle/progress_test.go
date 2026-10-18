package castle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"fmt"
	"testing"
	"time"
)

func request(seq uint64, fields ...uint64) []byte {
	out := wire.AppendVarint(nil, 1, seq)
	for i, v := range fields {
		out = wire.AppendVarint(out, i+2, v)
	}
	return out
}
func execute(t *testing.T, s *Service, ctx command.Context, path string, req []byte) []byte {
	t.Helper()
	_, out, handled, e := s.Handle(ctx, path, req)
	if e != nil || !handled {
		t.Fatalf("%s: handled=%v error=%v", path, handled, e)
	}
	return out
}
func testDesign() *gamedata.CastleRewardDesign {
	return &gamedata.CastleRewardDesign{
		TotalWarEnemies: []gamedata.TotalWarEnemy{{ID: 95, Level: 1}},
		PackID:          31,
		Towers:          map[[2]uint64]gamedata.CastleTower{{16, 1}: {Mode: 16, ID: 1, Deck: 61}, {16, 2}: {Mode: 16, ID: 2, Previous: 1, Deck: 62}},
		Floors:          map[uint64]gamedata.CastleFloor{1: {ID: 1, Boss: 52, Monsters: map[uint64]uint64{51: 61, 52: 62}, Scoring: gamedata.CastleScoring{Base: 10, Turn: 100, Survival: 100, BossMultiplier: 2}}},
		Decks:           map[uint64]gamedata.CastleDeck{61: {ID: 61, Challenges: []gamedata.CastleChallenge{{Type: 8, Reward: gamedata.Reward{Type: 4, Count: 10}}}}, 62: {ID: 62}},
		Characters:      map[uint64]gamedata.CastleCharacter{71: {Unique: 71, Element: 1}},
		Daily:           []gamedata.ScoreReward{{ID: 1, Score: 100, Reward: gamedata.Reward{Type: 4, Count: 1}}, {ID: 2, Score: 350, Reward: gamedata.Reward{Type: 4, Count: 3}}},
		TotalWarDaily:   []gamedata.ScoreReward{{ID: 1, Score: 50, Reward: gamedata.Reward{Type: 4, Count: 1}}, {ID: 2, Score: 100, Reward: gamedata.Reward{Type: 4, Count: 3}}},
		Reset:           gamedata.FieldResetSchedule{DailyReset: 9 * time.Hour},
	}
}

func TestBattleProgressReopenAccountsAndDailySeasonIsolation(t *testing.T) {
	d := testDesign()
	now := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	season := uint64(1)
	proof := battle.SettlementEvidence{}
	balances := map[string]map[[2]uint64]uint64{}
	gold := func(account string) uint64 { return balances[account][[2]uint64{4, 0}] }
	configure := func() *Service {
		s := NewService(d)
		s.AttachClock(func() time.Time { return now })
		s.AttachPeriod(func(command.Context) (uint64, bool, error) { return season, true, nil })
		s.AttachCurrentPack(func(command.Context) (int, error) { return int(d.PackID), nil })
		s.AttachEvidence(func(command.Context) battle.SettlementEvidence { return proof })
		s.AttachTotalWarValidation(func(_ command.Context, ps []Participant) error {
			for _, p := range ps {
				if p.CharacterIndex != 81 || p.CostumeID != 91 {
					return fmt.Errorf("not owned")
				}
			}
			return nil
		})
		s.AttachRewards(func(ctx command.Context, _ string, rewards []gamedata.Reward) ([]byte, error) {
			if balances[ctx.AccountID] == nil {
				balances[ctx.AccountID] = map[[2]uint64]uint64{}
			}
			for _, r := range rewards {
				balances[ctx.AccountID][[2]uint64{r.Type, r.ID}] += r.Count
			}
			return nil, nil
		})
		return s
	}
	s := configure()
	a := command.Context{Identity: command.Identity{AccountID: "a", SessionID: "a-session"}, State: stateio.NewMemory()}
	b := command.Context{Identity: command.Identity{AccountID: "b", SessionID: "b-session"}, State: stateio.NewMemory()}
	if out := execute(t, s, a, "/EvilCastleTowerInfo", request(1)); len(out) != 0 {
		t.Fatal("unplayed tower has progress")
	}
	if _, e := s.EnterBattle(a, request(2, 0, 0, 62, 16, 0, 0, 0, 2), "locked"); e == nil {
		t.Fatal("entered locked tower floor")
	}
	enter := request(3, 0, 0, 61, 16, 0, 0, 0, 1)
	if _, e := s.EnterBattle(a, enter, "tower"); e != nil {
		t.Fatal(e)
	}
	blue := wire.AppendVarint(nil, 2, 81)
	blue = wire.AppendVarint(blue, 3, 71)
	blue = wire.AppendVarint(blue, 6, 82)
	blue = wire.AppendVarint(blue, 7, 91)
	proof = battle.SettlementEvidence{Receipt: "tower", Mode: 16, Deck: 61, Round: 1, InitialBlue: [][]byte{blue}}
	end := wire.AppendVarint(request(4, 1), 5, 0)
	char := wire.AppendVarint(nil, 1, 81)
	char = wire.AppendVarint(char, 2, 71)
	char = wire.AppendVarint(char, 3, 1)
	end = wire.AppendBytes(end, 3, char)
	if _, e := s.CompleteBattle(a, end, "tower"); e != nil {
		t.Fatal(e)
	}
	if gold("a") != 10 || gold("b") != 0 {
		t.Fatal("tower challenge reward was not credited once to its account")
	}
	s = configure()
	a.SessionID = "reopened"
	assertTower := func(seq uint64) {
		t.Helper()
		rows, e := msgs(execute(t, s, a, "/EvilCastleTowerInfo", request(seq)), 1)
		if e != nil || len(rows) != 1 || n(rows[0], 1) != 1 || n(rows[0], 3) != 16 {
			t.Fatal("reopened tower has the wrong floor or mode")
		}
		index, present, e := wire.Varint(rows[0], 2)
		if e != nil || !present || index != 0 {
			t.Fatal("reopened tower lost its completed victory challenge")
		}
	}
	assertTower(5)
	if out := execute(t, s, b, "/EvilCastleTowerInfo", request(5)); len(out) != 0 {
		t.Fatal("account tower isolation failed")
	}
	start := request(6, 1)
	twchar := wire.AppendVarint(blue, 1, 1)
	start = wire.AppendBytes(start, 3, twchar)
	red := wire.AppendVarint(nil, 1, 2)
	red = wire.AppendVarint(red, 3, 95)
	red = wire.AppendVarint(red, 4, 1000000)
	red = wire.AppendVarint(red, 5, 1)
	start = wire.AppendBytes(start, 4, red)
	execute(t, s, a, "/TotalWarBattleStart", start)
	scoreBody := wire.AppendVarint(nil, 1, 91)
	scoreBody = wire.AppendVarint(scoreBody, 2, 100)
	twEnd := wire.AppendBytes(request(7), 2, scoreBody)
	execute(t, s, a, "/TotalWarBattleEnd", twEnd)
	if n(execute(t, s, a, "/TotalWarRewardState", request(8)), 1) != 1 {
		t.Fatal("scored total war reward unavailable")
	}
	if n(execute(t, s, b, "/TotalWarRewardState", request(8)), 1) != 0 {
		t.Fatal("account reward isolation failed")
	}
	claim := request(9)
	execute(t, s, a, "/TotalWarReward", claim)
	execute(t, s, a, "/TotalWarReward", claim)
	if gold("a") != 13 {
		t.Fatal("100 damage did not select the 3-gold tier exactly once")
	}
	if n(execute(t, s, a, "/TotalWarRewardState", request(10)), 1) != 0 {
		t.Fatal("claimed total war reward available")
	}
	a.SessionID = "second-login-same-day"
	if _, _, _, e := s.Handle(a, "/TotalWarReward", request(110)); e == nil || gold("a") != 13 {
		t.Fatal("same-day new login claimed the daily reward again")
	}
	now = now.Add(24 * time.Hour)
	s = configure()
	if n(execute(t, s, a, "/TotalWarRewardState", request(11)), 1) != 1 {
		t.Fatal("next day total war reward unavailable")
	}
	execute(t, s, a, "/TotalWarReward", request(111))
	if gold("a") != 16 || n(execute(t, s, a, "/TotalWarRewardState", request(112)), 1) != 0 {
		t.Fatal("new day did not credit exactly one renewed daily reward")
	}
	for _, monster := range []uint64{51, 52} {
		execute(t, s, a, "/EvilCastleEnter", request(12+monster, d.PackID, 1))
		deck := d.Floors[1].Monsters[monster]
		id := fmt.Sprint(monster)
		if _, e := s.EnterBattle(a, request(20+monster, 0, monster, deck, 7), id); e != nil {
			t.Fatal(e)
		}
		proof = battle.SettlementEvidence{Receipt: id, Mode: 7, Deck: deck, Monster: monster, Round: 1, InitialBlue: [][]byte{blue}}
		end := wire.AppendBytes(request(30+monster, 1), 3, char)
		if _, e := s.CompleteBattle(a, end, id); e != nil {
			t.Fatal(e)
		}
	}
	// One surviving hero at turn 1 earns 100 turn + 20 survival points.
	// The ordinary encounter scores 10+120; the boss scores 10+2*120.
	// Their independently computed total is 380, above the 350-point tier.
	info := execute(t, s, a, "/EvilCastleInfo", request(99))
	floors, e := msgs(info, 1)
	totals, te := msgs(info, 2)
	if e != nil || te != nil || len(floors) != 1 || len(totals) != 1 || n(floors[0], 2) != 1 || n(floors[0], 4) != 380 || n(floors[0], 5) != 380 || n(totals[0], 2) != 380 {
		t.Fatal("floor best and cumulative score disagree with the played encounters")
	}
	if n(execute(t, s, a, "/EvilCastleDailyRewardState", request(100)), 1) != 1 {
		t.Fatal("cleared floor reward unavailable")
	}
	execute(t, s, a, "/EvilCastleDailyReward", request(101))
	if gold("a") != 19 {
		t.Fatal("380 pride points did not credit the 3-gold daily tier")
	}
	if n(execute(t, s, a, "/EvilCastleDailyRewardState", request(102)), 1) != 0 {
		t.Fatal("castle claim not recorded")
	}
	season++
	now = now.Add(24 * time.Hour)
	if n(execute(t, s, a, "/EvilCastleDailyRewardState", request(103)), 1) != 0 {
		t.Fatal("expired season score granted reward")
	}
	newInfo := execute(t, s, a, "/EvilCastleInfo", request(105))
	newFloors, _ := msgs(newInfo, 1)
	newTotals, _ := msgs(newInfo, 2)
	if n(newInfo, 3) != 2 || len(newFloors) != 0 || len(newTotals) != 1 || n(newTotals[0], 2) != 0 || gold("a") != 19 || gold("b") != 0 {
		t.Fatal("new season retained expired score or changed account rewards")
	}
	assertTower(104)
}

func TestTotalWarRejectsUnstartedAndUnregisteredDamage(t *testing.T) {
	d := testDesign()
	s := NewService(d)
	s.AttachTotalWarValidation(func(_ command.Context, ps []Participant) error {
		for _, p := range ps {
			if p.CostumeID != 91 && p.CostumeID != 92 {
				return fmt.Errorf("not owned")
			}
		}
		return nil
	})
	ctx := command.Context{Identity: command.Identity{SessionID: "session"}, State: stateio.NewMemory()}
	end := wire.AppendBytes(request(1), 2, request(91, 100))
	if _, _, _, e := s.Handle(ctx, "/TotalWarBattleEnd", end); e == nil {
		t.Fatal("unstarted damage accepted")
	}
	char := wire.AppendVarint(nil, 1, 1)
	char = wire.AppendVarint(char, 2, 81)
	char = wire.AppendVarint(char, 3, 71)
	char = wire.AppendVarint(char, 6, 82)
	char = wire.AppendVarint(char, 7, 91)
	cheer := wire.AppendVarint(nil, 2, 81)
	cheer = wire.AppendVarint(cheer, 3, 71)
	cheer = wire.AppendVarint(cheer, 6, 83)
	cheer = wire.AppendVarint(cheer, 7, 92)
	cheer = wire.AppendVarint(cheer, 28, 1)
	start := wire.AppendBytes(request(2, 1), 3, char)
	start = wire.AppendBytes(start, 3, cheer)
	red := wire.AppendVarint(nil, 1, 2)
	red = wire.AppendVarint(red, 3, 95)
	red = wire.AppendVarint(red, 4, 1000000)
	red = wire.AppendVarint(red, 5, 1)
	start = wire.AppendBytes(start, 4, red)
	execute(t, s, ctx, "/TotalWarBattleStart", start)
	bad := wire.AppendBytes(request(3), 2, request(92, 100))
	if _, _, _, e := s.Handle(ctx, "/TotalWarBattleEnd", bad); e == nil {
		t.Fatal("cheering costume damage accepted")
	}
	good := wire.AppendBytes(request(4), 2, request(91, 100))
	execute(t, s, ctx, "/TotalWarBattleEnd", good)
	stored, e := msgs(execute(t, s, ctx, "/TotalWarInfo", request(5)), 1)
	if e != nil || len(stored) != 1 || n(stored[0], 1) != 91 || n(stored[0], 2) != 100 {
		t.Fatal("rejected damage consumed the active battle or changed its legitimate saved damage")
	}
}
