package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"errors"
	"testing"
)

func TestCollectedFieldObjectsAppearInDetailAndSummary(t *testing.T) {
	s := testService()
	s.packSummaryTargets = map[int]bool{21: true}
	s.packDetailDesign = func(int) (gamedata.PackDetailDesign, error) { return gamedata.PackDetailDesign{}, nil }
	s.WithFieldObjects(map[int]gamedata.FieldObjectDesign{21: {Objects: map[int]gamedata.FieldRewardObject{
		1001: {ID: 1001, GroupID: 101, Type: 2, ResetType: 1},
		1002: {ID: 1002, GroupID: 102, Type: 1, ResetType: 1},
	}}})
	for _, id := range []int{1001, 1002} {
		if err := s.state.MarkFieldRewardOpened(21, 0, id, "once"); err != nil {
			t.Fatal(err)
		}
	}
	req := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21)
	_, detail, _, err := s.Handle("/PackDetailInfo", req)
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	err = wire.Walk(detail, func(field wire.Field) error {
		if field.Number == 2 {
			rows++
		}
		return nil
	})
	if err != nil || rows != 2 {
		t.Fatalf("opened detail %x: %v", detail, err)
	}
	_, summary, _, err := s.Handle("/PackSummaryInfoList", req)
	if err != nil {
		t.Fatal(err)
	}
	row, _, _ := wire.Bytes(summary, 1)
	once, _, _ := wire.Varint(row, 2)
	regen, _, _ := wire.Varint(row, 3)
	if once != 1 || regen != 1 {
		t.Fatalf("collected counts %x", row)
	}
}

func TestPackDetailUsesVerifiedDesignAndRejectsUnavailableState(t *testing.T) {
	s := testService()
	s.packSummaryTargets = map[int]bool{21: true, 22: true}
	called := 0
	s.packDetailDesign = func(id int) (gamedata.PackDetailDesign, error) {
		called++
		if id != 21 {
			t.Fatalf("wrong pack %d", id)
		}
		return gamedata.PackDetailDesign{}, nil
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 10), 2, 21)
	if err := s.state.ClearQuest(1, 21); err != nil {
		t.Fatal(err)
	}
	code, response, handled, err := s.Handle("/PackDetailInfo", request)
	if err != nil || !handled || code != 627 || response == nil || len(response) != 0 || called != 1 {
		t.Fatalf("detail code=%d body=%x handled=%v err=%v calls=%d", code, response, handled, err, called)
	}
	s.packDetailDesign = func(int) (gamedata.PackDetailDesign, error) {
		return gamedata.PackDetailDesign{RegenMonsterIDs: []int{101}}, nil
	}
	_, _, handled, err = s.Handle("/PackDetailInfo", request)
	if !handled || !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsupported regeneration handled=%v err=%v", handled, err)
	}
	s.packDetailDesign = func(int) (gamedata.PackDetailDesign, error) {
		return gamedata.PackDetailDesign{}, errors.New("missing design")
	}
	if _, _, _, err := s.Handle("/PackDetailInfo", request); err == nil {
		t.Fatal("design failure swallowed")
	}
}

func TestPackDetailRejectsInvalidAndLockedRequestsBeforeReadingDesign(t *testing.T) {
	s := testService()
	s.packSummaryTargets = map[int]bool{21: true, 22: true}
	s.packDetailDesign = func(int) (gamedata.PackDetailDesign, error) {
		t.Fatal("invalid request read GameData")
		return gamedata.PackDetailDesign{}, nil
	}
	for _, request := range [][]byte{nil, {8}, wire.AppendVarint(nil, 1, 1), wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 22), wire.AppendVarint(wire.AppendVarint(nil, 1, 0x80000000), 2, 21)} {
		if _, _, handled, err := s.Handle("/PackDetailInfo", request); !handled || err == nil {
			t.Fatalf("request %x handled=%v err=%v", request, handled, err)
		}
	}
	delete(s.packSummaryTargets, 21)
	if _, _, _, err := s.Handle("/PackDetailInfo", wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21)); err == nil {
		t.Fatal("non-target pack accepted")
	}
}
