package eventgames

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"fmt"
	"testing"
	"time"
)

type resolve struct{ kind uint64 }

func (r resolve) Resolve(id uint64) (events.Schedule, error) {
	if id != 7 {
		return events.Schedule{}, fmt.Errorf("unknown")
	}
	return events.Schedule{UID: 7, Type: r.kind, ID: 1, Start: 1, End: 9999999999999}, nil
}

type economy struct{ spent, granted uint64 }

func (e *economy) ConsumeAndGrant(_ string, c []player.Item, r []gamedata.BattleReward) ([]byte, error) {
	for _, x := range c {
		e.spent += x.Count
	}
	for _, x := range r {
		e.granted += x.Count
	}
	return wire.AppendVarint(nil, 1, 1), nil
}
func fixture(t *testing.T, d *gamedata.EventGame) (*Service, *economy) {
	t.Helper()
	e := &economy{}
	s, err := Open(stateio.NewMemory(), "", "", resolve{d.Type}, e)
	if err != nil {
		t.Fatal(err)
	}
	s.load = func(uint64, uint64) (*gamedata.EventGame, error) { return d, nil }
	s.sample = func(n uint64) (uint64, error) {
		if n == 0 {
			return 0, fmt.Errorf("empty")
		}
		return 0, nil
	}
	s.now = func() time.Time { return time.UnixMilli(1000) }
	return s, e
}
func request(seq, n, cost uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, 7)
	item := wire.AppendVarint(nil, 1, 1)
	item = wire.AppendVarint(item, 2, 5)
	item = wire.AppendVarint(item, 3, 8)
	item = wire.AppendVarint(item, 4, cost)
	return wire.AppendBytes(b, int(n), item)
}
func TestBoardCostStateAndWholeReplyRetry(t *testing.T) {
	d := &gamedata.EventGame{ID: 1, Type: 12, Cost: 1, CostID: 5, CostType: 8, Cells: []gamedata.EventGameReward{{ID: 1}, {ID: 2, Rewards: []gamedata.BattleReward{{Type: 8, ID: 9, Count: 3}}}}, Moves: []struct{ ID, Min, Max uint64 }{{1, 1, 1}}}
	s, e := fixture(t, d)
	req := request(1, 3, 1)
	_, a, _, err := s.HandleSession("/MiniGameBoardPlay", req, "sid")
	if err != nil {
		t.Fatal(err)
	}
	_, b, _, err := s.HandleSession("/MiniGameBoardPlay", req, "sid")
	if err != nil || !bytes.Equal(a, b) || e.spent != 1 || e.granted != 3 {
		t.Fatalf("retry error=%v spent=%d granted=%d", err, e.spent, e.granted)
	}
	bad := wire.AppendVarint(req, 4, 1)
	if _, _, _, err = s.HandleSession("/MiniGameBoardPlay", bad, "sid"); err == nil {
		t.Fatal("changed retry accepted")
	}
	if s.state.Games["7"].Position != 1 {
		t.Fatal("move lost")
	}
}
func TestPuzzleWordRenewAndNoDuplicateOpen(t *testing.T) {
	d := &gamedata.EventGame{ID: 1, Type: 17, Cost: 1, CostID: 5, CostType: 8, Columns: 2, Cells: []gamedata.EventGameReward{{ID: 1, Count: 1, Slot: 12}, {ID: 2, Count: 1}, {ID: 1, Count: 2}}, Complete: []gamedata.EventGameReward{{ID: 1, Count: 1, Members: []uint64{1}, Rewards: []gamedata.BattleReward{{Type: 4, Count: 5}}}}}
	s, e := fixture(t, d)
	req := request(1, 4, 1)
	req = wire.AppendVarint(req, 3, 1)
	if _, _, _, err := s.HandleSession("/MiniPuzzleOpen", req, "sid"); err != nil {
		t.Fatal(err)
	}
	if e.spent != 1 || e.granted != 5 {
		t.Fatal("word award missing")
	}
	bad := request(2, 4, 1)
	bad = wire.AppendVarint(bad, 3, 1)
	if _, _, _, err := s.HandleSession("/MiniPuzzleOpen", bad, "sid"); err == nil {
		t.Fatal("duplicate tile accepted")
	}
	renew := wire.AppendVarint(nil, 1, 3)
	renew = wire.AppendVarint(renew, 2, 7)
	if _, _, _, err := s.HandleSession("/MiniPuzzleRenew", renew, "sid"); err != nil {
		t.Fatal(err)
	}
	if s.state.Games["7"].Clear != 2 || e.spent != 1 {
		t.Fatal("renew cost/stage wrong")
	}
}
func TestRouletteFreeCountAndMilestone(t *testing.T) {
	d := &gamedata.EventGame{ID: 1, Type: 19, Cost: 1, CostID: 5, CostType: 8, Free: 1, Cells: []gamedata.EventGameReward{{ID: 1, Weight: 1, Rewards: []gamedata.BattleReward{{Count: 2}}}}, Complete: []gamedata.EventGameReward{{ID: 1, Count: 1, Rewards: []gamedata.BattleReward{{Count: 3}}}}}
	s, e := fixture(t, d)
	req := wire.AppendVarint(nil, 1, 1)
	req = wire.AppendVarint(req, 2, 7)
	req = wire.AppendVarint(req, 4, 1)
	if _, _, _, err := s.HandleSession("/MiniGameRouletteDraw", req, "sid"); err != nil {
		t.Fatal(err)
	}
	if e.spent != 0 || e.granted != 5 {
		t.Fatal("free rewards wrong")
	}
	req, _, _ = wire.ReplaceVarint(req, 1, 2)
	if _, _, _, err := s.HandleSession("/MiniGameRouletteDraw", req, "sid"); err == nil {
		t.Fatal("extra free draw accepted")
	}
}
func TestBingoLinesUseProtocolEnums(t *testing.T) {
	g := GameState{Board: make([]uint64, 9), Opened: []uint64{0, 4, 8}}
	if !completeLine(&g, 3, 0, 0) || completeLine(&g, 3, 1, 0) {
		t.Fatal("diagonal/row enum wrong")
	}
	g.Opened = []uint64{0, 1, 2}
	if !completeLine(&g, 3, 1, 0) {
		t.Fatal("row wrong")
	}
	g.Opened = []uint64{0, 3, 6}
	if !completeLine(&g, 3, 2, 0) {
		t.Fatal("column wrong")
	}
}

func TestBingoLineRewardExactlyOnceAndBeforeAfterWire(t *testing.T) {
	d := &gamedata.EventGame{ID: 1, Type: 13, Columns: 2, Cells: []gamedata.EventGameReward{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}, Lines: []gamedata.EventGameReward{{Count: 0, LineType: 1, LineIndex: 0, Rewards: []gamedata.BattleReward{{Count: 7}}}}}
	s, _ := fixture(t, d)
	g := GameState{UID: 7, Type: 13, ID: 1, Board: []uint64{1, 2, 3, 4}, Opened: []uint64{0}}
	body, _, rs, err := s.play("/MiniGameBingoPlay", wire.AppendVarint(nil, 4, 1), &g, d)
	if err != nil || len(rs[6]) != 1 {
		t.Fatalf("line reward %v %+v", err, rs)
	}
	line, found, e := wire.Bytes(body, 4)
	if e != nil || !found || scalar(line, 1) != 1 || scalar(line, 2) != 0 {
		t.Fatalf("line wire %x", body)
	}
	_, _, rs, err = s.play("/MiniGameBingoPlay", wire.AppendVarint(nil, 4, 1), &g, d)
	if err != nil || len(rs[6]) != 0 {
		t.Fatal("same line granted twice")
	}
}
func TestMiniGameSamplerOutOfRangeRejected(t *testing.T) {
	d := &gamedata.EventGame{ID: 1, Type: 13, Columns: 2, Cells: []gamedata.EventGameReward{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}}
	s, _ := fixture(t, d)
	s.sample = func(n uint64) (uint64, error) { return n, nil }
	g := GameState{}
	if err := s.initialize(&g, d); err == nil {
		t.Fatal("shuffle accepted bad RNG")
	}
	g.Board = []uint64{1, 2, 3, 4}
	if _, _, _, err := s.play("/MiniGameBingoPlay", wire.AppendVarint(nil, 4, 1), &g, d); err == nil {
		t.Fatal("draw accepted bad RNG")
	}
}

func TestSavedMiniGameBoardAndCompletedLineRejected(t *testing.T) {
	d := &gamedata.EventGame{ID: 1, Type: 13, Columns: 2, Cells: []gamedata.EventGameReward{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}, Lines: []gamedata.EventGameReward{{LineType: 1, LineIndex: 0}}}
	g := GameState{UID: 7, Type: 13, ID: 1, Board: []uint64{1, 2, 3}}
	if e := validateSaved("7", g, d); e == nil {
		t.Fatal("truncated board accepted")
	}
	g.Board = append(g.Board, 4)
	g.Lines = []uint64{1000}
	if e := validateSaved("7", g, d); e == nil {
		t.Fatal("uncompleted line claimed")
	}
	g.Opened = []uint64{0, 1}
	if e := validateSaved("7", g, d); e != nil {
		t.Fatal(e)
	}
}

func TestRoulettePityAndAccumulatedWire(t *testing.T) {
	d := &gamedata.EventGame{ID: 1, Type: 19, RewardGroup: 77, Pity: 3, Cells: []gamedata.EventGameReward{{ID: 1, Weight: 100, Weight2: 100}, {ID: 2, Slot: 1, Weight: 1, Weight2: 1}}, Complete: []gamedata.EventGameReward{{Count: 3, Rewards: []gamedata.BattleReward{{Count: 5}}}}}
	s, _ := fixture(t, d)
	g := GameState{UID: 7, Type: 19, ID: 1, Tries: 2, SinceSpecial: 2}
	req := wire.AppendVarint(wire.AppendVarint(nil, 3, 1), 4, 1)
	body, _, rewards, e := s.play("/MiniGameRouletteDraw", req, &g, d)
	if e != nil || !g.Special || g.SinceSpecial != 0 || g.Tries != 3 || len(rewards[3]) != 1 {
		t.Fatalf("pity state %+v rewards%v err%v", g, rewards, e)
	}
	info, found, _ := wire.Bytes(body, 4)
	group, _, _ := wire.Varint(info, 1)
	id, _, _ := wire.Varint(info, 2)
	if !found || group != 77 || id != 2 {
		t.Fatalf("roulette field4 %x", body)
	}
}
