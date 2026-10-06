package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
	"time"
)

func TestPackDetailTracksKillRetryRespawnAndResearchAcrossRestart(t *testing.T) {
	s := testService()
	store := stateio.NewMemory()
	if err := s.AttachFieldMonsterState(store); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s.monsterNow = func() time.Time { return now }
	s.packSummaryTargets = map[int]bool{21: true}
	s.packDetailDesign = func(int) (gamedata.PackDetailDesign, error) {
		return gamedata.PackDetailDesign{RegenMonsterIDs: []int{9}}, nil
	}
	s.monsterLoader = func(int) ([]gamedata.FieldMonsterDesign, error) {
		return []gamedata.FieldMonsterDesign{{ID: 9, GroupID: 7, BattleDeck: 33, RegenSeconds: 10}}, nil
	}
	if e := s.state.MarkResearchObject(21, 42, nil); e != nil {
		t.Fatal(e)
	}
	req := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21)
	_, before, _, e := s.Handle("/PackDetailInfo", req)
	if e != nil {
		t.Fatal(e)
	}
	research, _, _ := wire.Varint(before, 3)
	if research != 42 {
		t.Fatal("detail omitted researched object")
	}
	first, handled, e := s.BeginFieldMonsterBattle(21, 9, 33)
	if e != nil || !handled {
		t.Fatal(e)
	}
	dead, e := s.CompleteFieldMonsterBattle(21, 9, first)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.CompleteFieldMonsterBattle(21, 9, first)
	if e != nil || !bytes.Equal(dead, again) {
		t.Fatal("kill retry changed regeneration time", e)
	}
	if _, _, e = s.BeginFieldMonsterBattle(21, 9, 33); e == nil {
		t.Fatal("defeated monster entered before respawn")
	}
	_, detail, _, e := s.Handle("/PackDetailInfo", req)
	if e != nil {
		t.Fatal(e)
	}
	row, _, _ := wire.Bytes(detail, 1)
	respawn, _, _ := wire.Varint(row, 3)
	if respawn != uint64(now.Add(10*time.Second).UnixMilli()) {
		t.Fatal("wrong respawn deadline")
	}
	_, summary, _, e := s.Handle("/PackSummaryInfoList", req)
	if e != nil {
		t.Fatal(e)
	}
	row, _, _ = wire.Bytes(summary, 1)
	count, _, _ := wire.Varint(row, 4)
	if count != 1 {
		t.Fatal("summary omitted defeated monster")
	}
	now = now.Add(11 * time.Second)
	next, _, e := s.BeginFieldMonsterBattle(21, 9, 33)
	if e != nil || next == first {
		t.Fatal("new regeneration did not create distinct reward identity", e)
	}
	if _, e = s.CompleteFieldMonsterBattle(21, 9, first); e != nil {
		t.Fatal("delayed receipt retry rejected", e)
	}
	v, e := s.loadMonsterState()
	if e != nil {
		t.Fatal(e)
	}
	if v.Monsters[monsterKey(21, 0, 9)].Defeated {
		t.Fatal("old retry killed new generation")
	}
}
