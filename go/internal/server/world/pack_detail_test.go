package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"errors"
	"testing"
)

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
