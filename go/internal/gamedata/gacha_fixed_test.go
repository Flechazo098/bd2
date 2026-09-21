package gamedata

import "testing"

func TestCostumeGuaranteedFourAndFiveCountsAcrossOneAndTen(t *testing.T) {
	pool := []WeightedCostume{
		{Weight: 150, Children: []WeightedCostume{{ID: 5001, Weight: 1}}},
		{Weight: 150, Children: []WeightedCostume{{ID: 5002, Weight: 1}}},
		{Weight: 1400, Children: []WeightedCostume{{ID: 4001, Weight: 1}}},
		{Weight: 8300, Children: []WeightedCostume{{ID: 3001, Weight: 1}}},
	}
	fixed := GachaFixedDesign{ID: 1, CostumeGrade4Count: 10, CostumeGrade5Count: 100, ResetOnMatchingGrade: true}
	chooseThree := func(limit uint64) (uint64, error) {
		if limit == 10000 {
			return 9999, nil
		}
		return 0, nil
	}
	// The first of a ten-pull hits the shared four-star boundary after nine
	// unsuccessful singles, and the next nine slots continue the new count.
	gacha := RegularGacha{ID: 1, Count: 10, PriceType: 3, Price: 2000, Pool: pool}
	roll, state, err := gacha.rollWithCostumeFixed(9, 25, fixed, nil, nil, chooseThree)
	if err != nil {
		t.Fatal(err)
	}
	if len(roll) != 10 || roll[0] != 4001 || state.CostumeGrade4Sort != 0 || state.CostumeGrade4Count != 9 || state.CostumeGrade5Count != 35 || state.CostumeGrade5Sort != -1 {
		t.Fatalf("four-star boundary roll=%v state=%+v", roll, state)
	}
	// When both boundaries coincide, the five-star guarantee has priority;
	// it resets both counters and is sampled from the genuine 50/50 5-star
	// branches, never from a synthetic hardcoded character.
	roll, state, err = gacha.rollWithCostumeFixed(9, 99, fixed, nil, nil, chooseThree)
	if err != nil {
		t.Fatal(err)
	}
	if roll[0] != 5001 || state.CostumeGrade5Sort != 0 || state.CostumeGrade4Sort != -1 || state.CostumeGrade4Count != 9 || state.CostumeGrade5Count != 9 {
		t.Fatalf("five-star priority roll=%v state=%+v", roll, state)
	}
	// Natural five-stars reset both counters without falsely reporting a
	// guaranteed slot in GachaFixedDBInfo.ApplySortId.
	gacha.Count = 1
	chooseFive := func(limit uint64) (uint64, error) { return 0, nil }
	roll, state, err = gacha.rollWithCostumeFixed(8, 98, fixed, nil, nil, chooseFive)
	if err != nil || roll[0] != 5001 || state.CostumeGrade4Count != 0 || state.CostumeGrade5Count != 0 || state.CostumeGrade4Sort != -1 || state.CostumeGrade5Sort != -1 {
		t.Fatalf("natural five-star roll=%v state=%+v err=%v", roll, state, err)
	}
}

func TestTwelvePickSelectionAppliesToNaturalAndPityFiveStar(t *testing.T) {
	pool := []WeightedCostume{
		{Weight: 300, Children: []WeightedCostume{{ID: 5001, Weight: 1}}},
		{Weight: 1400, Children: []WeightedCostume{{ID: 4001, Weight: 1}}},
		{Weight: 8300, Children: []WeightedCostume{{ID: 3001, Weight: 1}}},
	}
	gacha := RegularGacha{ID: 101, Count: 1, PriceType: 3, Price: 200, Pool: pool}
	fixed := GachaFixedDesign{ID: 8, CostumeGrade4Count: 10, CostumeGrade5Count: 100, ResetOnMatchingGrade: true}
	selected := []uint64{5012, 5013}
	chooseFirst := func(limit uint64) (uint64, error) { return 0, nil }
	roll, state, err := gacha.rollWithCostumeFixed(0, 0, fixed, selected, selected, chooseFirst)
	if err != nil || len(roll) != 1 || roll[0] != 5012 || len(state.SelectionSorts) != 1 || state.SelectionSorts[0] != 0 || state.CostumeGrade5Sort != -1 {
		t.Fatalf("ordinary selected five roll=%v state=%+v err=%v", roll, state, err)
	}
	roll, state, err = gacha.rollWithCostumeFixed(9, 99, fixed, selected, selected, chooseFirst)
	if err != nil || len(roll) != 1 || roll[0] != 5012 || state.CostumeGrade5Sort != 0 || len(state.SelectionSorts) != 1 || state.SelectionSorts[0] != 0 {
		t.Fatalf("guaranteed selected five roll=%v state=%+v err=%v", roll, state, err)
	}
	chooseThree := func(limit uint64) (uint64, error) {
		if limit == 10000 {
			return 9999, nil
		}
		return 0, nil
	}
	roll, state, err = gacha.rollWithCostumeFixed(0, 0, fixed, selected, selected, chooseThree)
	if err != nil || roll[0] != 3001 || len(state.SelectionSorts) != 0 || state.CostumeGrade4Count != 1 || state.CostumeGrade5Count != 1 {
		t.Fatalf("ordinary three roll=%v state=%+v err=%v", roll, state, err)
	}
}
