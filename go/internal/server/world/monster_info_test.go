package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
)

// This regression exercises the missing field entry route with duplicate packed
// regeneration IDs, and verifies that other groups cannot leak into the map.
func TestMonsterInfoRegenerationGroupsAndQuestGate(t *testing.T) {
	s := testService()
	s.monsterLoader = func(pack int) ([]gamedata.FieldMonsterDesign, error) {
		if pack != 21 {
			t.Fatalf("loaded wrong pack %d", pack)
		}
		return []gamedata.FieldMonsterDesign{{ID: 1001, GroupID: 7}, {ID: 1002, GroupID: 7, QuestID: 2, BattleDeck: 77}, {ID: 1003, GroupID: 8}}, nil
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendBytes(request, 2, []byte{7, 7})
	code, body, handled, err := s.Handle("/MonsterInfo", request)
	if err != nil || !handled || code != 51 {
		t.Fatalf("MonsterInfo: %d %v %v", code, handled, err)
	}
	var ids []uint64
	var active []uint64
	err = wire.Walk(body, func(f wire.Field) error {
		id, _, e := wire.Varint(f.Value, 1)
		if e != nil {
			return e
		}
		a, _, e := wire.Varint(f.Value, 6)
		ids = append(ids, id)
		active = append(active, a)
		return e
	})
	if err != nil || len(ids) != 2 || ids[0] != 1001 || ids[1] != 1002 || active[0] != 1 || active[1] != 0 {
		t.Fatalf("wrong field monsters %v active %v: %v", ids, active, err)
	}
	_, retry, _, err := s.Handle("/MonsterInfo", request)
	if err != nil || !bytes.Equal(body, retry) {
		t.Fatal("reading stable field monsters changed the response")
	}
	unpacked := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 7)
	_, alternate, _, err := s.Handle("/MonsterInfo", unpacked)
	if err != nil || !bytes.Equal(body, alternate) {
		t.Fatal("unpacked protobuf group IDs changed results")
	}
	if _, _, _, err = s.Handle("/MonsterInfo", wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 2, []byte{128})); err == nil {
		t.Fatal("accepted truncated group ID")
	}
}

func TestMonsterLifetimeSurvivesReaderRestartWithoutRenewal(t *testing.T) {
	s := testService()
	store := stateio.NewMemory()
	if err := s.AttachFieldMonsterState(store); err != nil {
		t.Fatal(err)
	}
	s.monsterLoader = func(int) ([]gamedata.FieldMonsterDesign, error) {
		return []gamedata.FieldMonsterDesign{{ID: 9, GroupID: 7, LifeSeconds: 30}}, nil
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 7)
	_, first, _, err := s.Handle("/MonsterInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	s2 := testService()
	s2.monsterLoader = s.monsterLoader
	s2.AttachFieldMonsterState(store)
	_, again, _, err := s2.Handle("/MonsterInfo", request)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("restart renewed finite monster lifetime")
	}
	if err := store.Save("fieldmonsters", []byte(`{"21/0/9":1}`)); err != nil {
		t.Fatal(err)
	}
	_, expired, _, err := s2.Handle("/MonsterInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	row, _, err := wire.Bytes(expired, 1)
	if err != nil {
		t.Fatal(err)
	}
	active, _, _ := wire.Varint(row, 6)
	if active != 0 {
		t.Fatal("expired monster revived by reading info")
	}
}
