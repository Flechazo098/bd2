package pictorial

import (
	"errors"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
)

func TestMaximumHealthIncludesOwnedEquipmentPotentialAndAwakening(t *testing.T) {
	character := player.Character{InvenIndex: 77, ID: 350, Level: 1}
	s := &Service{Design: &gamedata.PictorialDesign{}, Owned: &ownedState{}}
	s.baseHealth.Store([2]uint64{350, 1}, float64(100))
	s.EquipmentContributions = func(got player.Character) ([]gamedata.StatContribution, error) {
		if got.InvenIndex != character.InvenIndex || got.ID != character.ID || got.Level != character.Level {
			t.Fatalf("wrong stat character: %+v", got)
		}
		return []gamedata.StatContribution{{Stat: gamedata.StatHealth, Flat: 20, Percent: .1}}, nil
	}
	s.PotentialContributions = func(player.Character) ([]gamedata.StatContribution, error) {
		return []gamedata.StatContribution{{Stat: gamedata.StatHealth, Percent: .2}}, nil
	}
	s.AwakeContributions = func(player.Character) ([]gamedata.StatContribution, error) {
		return []gamedata.StatContribution{{Stat: gamedata.StatHealth, Flat: 5}}, nil
	}
	maximum, err := s.MaxHealth(character)
	if err != nil || maximum != 162 {
		t.Fatalf("maximum=%d err=%v, want trunc((100+20+5)*1.3)=162", maximum, err)
	}
	s.EquipmentContributions = func(player.Character) ([]gamedata.StatContribution, error) {
		return nil, errors.New("equipment unavailable")
	}
	if _, err := s.MaxHealth(character); err == nil {
		t.Fatal("ignored equipment stat failure")
	}
}
