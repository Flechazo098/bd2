//go:build integration

package roguelike

import (
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

type testEconomy struct {
	rewards int
	calls   int
}

func (e *testEconomy) Apply(_ command.Context, _ string, _ []gamedata.Reward, rewards []gamedata.Reward) ([]byte, error) {
	e.calls++
	for _, r := range rewards {
		e.rewards += int(r.Count)
	}
	return []byte{8, 1}, nil
}
func currentDesign(t *testing.T) *gamedata.RogueLikeDesign {
	t.Helper()
	v, e := versionconfig.Find()
	if e != nil {
		t.Fatal(e)
	}
	versionconfig.Use(v)
	d, e := gamedata.LoadRogueLikeDesign(v.Resolve("data/resources/GameData"), v.GameDataVersion)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func req(seq uint64, fields ...uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	for i, v := range fields {
		b = wire.AppendVarint(b, i+2, v)
	}
	return b
}
func rpc(t *testing.T, s *Service, ctx command.Context, op string, b []byte) []byte {
	t.Helper()
	_, out, ok, e := s.Handle(ctx, "/EvilCastleRogueLike"+op, b)
	if e != nil || !ok {
		t.Fatalf("%s handled=%t err=%v", op, ok, e)
	}
	return out
}
func firstMessage(t *testing.T, b []byte, f int) []byte {
	t.Helper()
	a, e := messages(b, f)
	if e != nil || len(a) == 0 {
		t.Fatalf("missing message field %d: %v", f, e)
	}
	return a[0]
}
func assertRejected(t *testing.T, s *Service, ctx command.Context, op string, b []byte) {
	t.Helper()
	_, _, ok, e := s.Handle(ctx, "/EvilCastleRogueLike"+op, b)
	if !ok || e == nil {
		t.Fatalf("%s accepted invalid command", op)
	}
}

// The current client contracts are RogueLikePacket (initial draft and reward
// deltas), RogueLikeMainUI.GetConnectRoomList, and BattlePacket (normal battle
// transport). This exercise uses real versioned rules, not response fixtures.
func TestRunReopenBattleSettlementAndFirstClearCannotPayTwice(t *testing.T) {
	d := currentDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{AccountID: "rogue-a", SessionID: "login-a"}, State: store}
	period := Period{Season: 1, Regular: 1, Active: true}
	economy := &testEconomy{}
	tickets := uint64(0)
	proof := battle.SettlementEvidence{}
	open := func() *Service {
		s, e := Open(stateio.EntrySnapshotStore{Domain: "battle", Bucket: "gameplay"}, d, Options{Access: func(command.Context) error { return nil }, Economy: economy, Owner: 1, Period: func(command.Context) (Period, error) { return period, nil }, Tickets: func(_ command.Context, _ string, count uint64) error { tickets += count; return nil }, Evidence: func(command.Context) battle.SettlementEvidence { return proof }})
		if e != nil {
			t.Fatal(e)
		}
		s.sample = func(uint64) uint64 { return 0 }
		return s
	}
	s := open()
	seq := uint64(1)
	assertRejected(t, s, ctx, "QuickBattle", req(seq, 1, 1))
	seq++
	enterReq := req(seq, 1)
	enter := rpc(t, s, ctx, "Enter", enterReq)
	if !bytes.Equal(enter, rpc(t, s, ctx, "Enter", enterReq)) || tickets == 0 {
		t.Fatal("entry retry changed map or lost ticket charge")
	}
	charged := tickets
	seq++
	assertRejected(t, s, ctx, "Enter", req(seq, 1))
	seq++
	draftUnique := map[uint64]bool{}
	for range 5 {
		info := rpc(t, s, ctx, "Info", req(seq))
		seq++
		choice := firstMessage(t, info, 8)
		ids := []uint64{}
		_ = wire.Walk(choice, func(f wire.Field) error {
			if f.Number == 2 {
				value, k := binary.Uvarint(f.Value)
				if k <= 0 {
					return wire.ErrMalformed
				}
				ids = append(ids, value)
			}
			return nil
		})
		if len(ids) == 0 {
			t.Fatal("initial draft ended before the client's five starting costumes")
		}
		id := ids[0]
		for _, candidate := range ids {
			unique := d.Costumes[candidate].Unique
			if !draftUnique[unique] {
				id = candidate
				draftUnique[unique] = true
				break
			}
		}
		rpc(t, s, ctx, "SelectReward", req(seq, 1, 0, id))
		seq++
		s = open()
	}
	info := rpc(t, s, ctx, "Info", req(seq))
	seq++
	costumes, _ := messages(info, 6)
	chars, _ := messages(info, 5)
	if len(costumes) != 5 || len(chars) == 0 {
		t.Fatal("reopening lost the starting roster")
	}
	if n(firstMessage(t, info, 1), 3) != 0 {
		t.Fatal("draft does not lead to the map")
	}
	formation := req(seq)
	seq++
	for i, c := range chars {
		a := wire.AppendVarint(nil, 1, n(c, 1))
		a = wire.AppendVarint(a, 2, uint64(i))
		a = wire.AppendVarint(a, 3, uint64(i+1))
		formation = wire.AppendBytes(formation, 2, a)
	}
	rpc(t, s, ctx, "DeckSave", formation)
	for battleNo := range 40 {
		v, _, e := s.load(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if v.Run == nil {
			break
		}
		r := v.Run
		if r.State == 1 || r.State == 2 {
			rpc(t, s, ctx, "SelectReward", req(seq, r.ChoiceType, 0, r.Choice[0]))
			seq++
			continue
		}
		if r.State != 0 {
			t.Fatalf("unexpected map state %d", r.State)
		}
		rpc(t, s, ctx, "RoomEnter", req(seq, 1))
		seq++
		v, _, _ = s.load(ctx)
		r = v.Run
		if r.State == 4 {
			rpc(t, s, ctx, "ShopExit", req(seq))
			seq++
			continue
		}
		if r.State == 1 || r.State == 2 {
			continue
		}
		if r.State == 5 {
			rpc(t, s, ctx, "EventSelect", req(seq, 1))
			seq++
			continue
		}
		deck, mode := s.battleRule(r)
		identity := fmt.Sprintf("fight-%d", battleNo)
		if _, e = s.EnterBattle(ctx, req(seq, 0, 0, deck, mode), identity); e != nil {
			t.Fatalf("enter battle floor=%d state=%d room=%d: %v", r.Floor, r.State, r.Room, e)
		}
		seq++
		proof = battle.SettlementEvidence{Receipt: identity, Mode: mode, Deck: deck, Round: 0}
		end := req(seq, 1)
		seq++
		if _, e = s.CompleteBattle(ctx, end, identity); e == nil {
			t.Fatal("settled a room without any started battle")
		}
		var blue [][]byte
		for i, c := range r.Characters {
			var costume costume
			for _, a := range r.Costumes {
				if a.Index == c.Costume {
					costume = a
				}
			}
			a := wire.AppendVarint(nil, 1, uint64(i+1))
			a = wire.AppendVarint(a, 2, c.Index)
			a = wire.AppendVarint(a, 3, c.ID)
			a = wire.AppendVarint(a, 4, c.HP)
			a = wire.AppendVarint(a, 5, d.Default.V(7))
			a = wire.AppendVarint(a, 6, costume.Index)
			a = wire.AppendVarint(a, 7, costume.ID)
			a = wire.AppendVarint(a, 8, costume.Level)
			a = wire.AppendVarint(a, 9, uint64(i))
			blue = append(blue, a)
			end = wire.AppendBytes(end, 3, charWire(c, d.Default.V(7)))
		}
		proof.Round = 1
		proof.InitialBlue = blue
		out, e := s.CompleteBattle(ctx, end, identity)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.CompleteBattle(ctx, end, identity); e == nil {
			t.Fatal("domain paid a settled room twice")
		}
		if len(out) == 0 {
			t.Fatal("battle omitted client state")
		}
		s = open()
	}
	v, _, e := s.load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if v.Run != nil || v.MaxTry == 0 || v.MaxReward == 0 || economy.rewards == 0 {
		t.Fatal("full run did not unlock sweep and credit first-clear rewards")
	}
	paid := economy.rewards
	obs := v.Obsidian
	quick := req(seq, 1, 2)
	seq++
	reply := rpc(t, s, ctx, "QuickBattle", quick)
	if !bytes.Equal(reply, rpc(t, s, ctx, "QuickBattle", quick)) {
		t.Fatal("sweep retry changed receipt")
	}
	if tickets <= charged || economy.rewards != paid {
		t.Fatal("sweep did not consume tickets or repeated first-clear inventory rewards")
	}
	s = open()
	v, _, _ = s.load(ctx)
	if v.Obsidian <= obs {
		t.Fatal("sweep growth currency was lost after reopen")
	}
	assertRejected(t, s, ctx, "QuickBattle", req(seq, 2, 1))
	seq++
	other := ctx
	other.State = stateio.NewMemory()
	other.AccountID = "rogue-b"
	assertRejected(t, s, other, "QuickBattle", req(seq, 1, 1))
}
