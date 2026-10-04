package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"testing"
	"time"
)

func TestEquipmentStatContributionsUseOnlyEquippedOwnerAndAllHealthOptions(t *testing.T) {
	s := &EquipmentInventory{owned: equipmentSnapshot{Equipment: []Equipment{
		{InvenIndex: 1, UseChar: 77, Level: 1, Rank: []uint64{1, 0, 0}, MainOption: []EquipmentOption{{GroupID: 10, ID: 1}, {GroupID: 999, ID: 3}}, SubOption: []EquipmentOption{{GroupID: 20, ID: 2}}, PrivateOption: &EquipmentOption{GroupID: 10, ID: 1}},
		{InvenIndex: 2, UseChar: 78, MainOption: []EquipmentOption{{GroupID: 999, ID: 1}}},
		{InvenIndex: 3, MainOption: []EquipmentOption{{GroupID: 999, ID: 1}}},
	}}}
	d := &gamedata.EquipmentStatDesign{Options: map[[2]uint64]gamedata.EquipmentStatRule{
		{10, 1}: {Default: 7.9, Growth: 2, Levels: []float64{0, 1}, Ranks: [3][]float64{{0.5}, nil, nil}},
		{20, 2}: {Default: 0.01359, Growth: 100},
	}}
	if err := s.AttachStatDesign(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.StatContributions(Character{InvenIndex: 77})
	if err != nil || len(got) != 3 {
		t.Fatalf("contributions=%+v err=%v", got, err)
	}
	if hp := gamedata.AggregateStats(gamedata.BaseStats{Health: 100}, got).Health; hp != 121 {
		t.Fatalf("equipped health=%v", hp)
	}
	// A normal Find callback must safely enter equipment snapshot resolution.
	_, _, characters := foodTestService(t, stateio.NewMemory())
	if err := characters.AttachMaxHealth(func(c Character) (uint64, error) {
		contributions, err := s.StatContributions(c)
		return uint64(gamedata.AggregateStats(gamedata.BaseStats{Health: 100}, contributions).Health), err
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan Character, 1)
	go func() { c, _ := characters.Find(77); done <- c }()
	select {
	case c := <-done:
		if c.HP != 121 {
			t.Fatalf("Find health=%d", c.HP)
		}
	case <-time.After(time.Second):
		t.Fatal("Find deadlocked calculating equipped health")
	}
}

func TestEatFoodWithRankedEquipmentAndEmptyHealthOptionRankCurves(t *testing.T) {
	food, inventory, characters := foodTestService(t, stateio.NewMemory())
	equipment := &EquipmentInventory{owned: equipmentSnapshot{Equipment: []Equipment{{
		InvenIndex: 1, UseChar: 77, Level: 1, Rank: []uint64{2, 2, 2},
		MainOption: []EquipmentOption{{GroupID: 10, ID: 1}},
	}}}}
	if err := equipment.AttachStatDesign(&gamedata.EquipmentStatDesign{Options: map[[2]uint64]gamedata.EquipmentStatRule{
		{10, 1}: {Default: 10, Growth: 2, Levels: []float64{0, 1}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachMaxHealth(func(c Character) (uint64, error) {
		contributions, err := equipment.StatContributions(c)
		if err != nil {
			return 0, err
		}
		return uint64(gamedata.AggregateStats(gamedata.BaseStats{Health: 100}, contributions).Health), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := characters.SetCurrentHealth(77, 20); err != nil {
		t.Fatal(err)
	}
	stacks, err := inventory.GrantOnce("ranked-equipment-food", []gamedata.BattleReward{{Type: 5, ID: 105, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := food.Handle("/EatFood", foodRequest(1, 77, 21, stacks[0])); err != nil {
		t.Fatal(err)
	}
	// Base 100 + equipment 12 = 112 maximum; the 50% dish heals 56.
	if hp, err := characters.CurrentHealth(77); err != nil || hp != 76 {
		t.Fatalf("healed health=%d err=%v", hp, err)
	}
	if len(inventory.All()) != 0 {
		t.Fatal("successful recovery did not consume dish")
	}
}
