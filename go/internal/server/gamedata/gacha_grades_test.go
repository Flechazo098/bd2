package gamedata

import "testing"

func TestNonResettingFixedGuaranteeKeepsNaturalFiveStarProgress(t *testing.T) {
	g := RegularGacha{Count: 10, Grades: map[uint64]uint64{55: 5}, Pool: []WeightedCostume{{ID: 55, Weight: 1}}}
	_, state, err := g.rollWithCostumeFixed(0, 0, GachaFixedDesign{ID: 3, CostumeGrade5Count: 10}, nil, []uint64{55}, func(uint64) (uint64, error) { return 0, nil })
	if err != nil || state.CostumeGrade5Sort != 9 || state.CostumeGrade5Count != 0 || len(state.SelectionSorts) != 1 || state.SelectionSorts[0] != 9 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestCostumeFixedUsesActualGradesAcrossMixedAndReorderedBranches(t *testing.T) {
	g := RegularGacha{Count: 1, Grades: map[uint64]uint64{99: 3, 77: 5, 66: 4, 55: 5}, Pool: []WeightedCostume{
		{Weight: 10, Children: []WeightedCostume{{ID: 99, Weight: 3}, {ID: 77, Weight: 1}}},
		{ID: 66, Weight: 7}, {ID: 55, Weight: 5},
	}}
	fixed := GachaFixedDesign{ResetOnMatchingGrade: true, ID: 1, CostumeGrade4Count: 10, CostumeGrade5Count: 100}
	first := func(uint64) (uint64, error) { return 0, nil }
	roll, state, err := g.rollWithCostumeFixed(9, 99, fixed, nil, nil, first)
	if err != nil || roll[0] != 55 || state.CostumeGrade5Count != 0 {
		t.Fatalf("five=%v state=%+v err=%v", roll, state, err)
	}
	roll, state, err = g.rollWithCostumeFixed(9, 0, fixed, nil, nil, first)
	if err != nil || roll[0] != 66 || state.CostumeGrade5Count != 1 {
		t.Fatalf("four=%v state=%+v err=%v", roll, state, err)
	}
	// Last branch is five-star despite the historical assumption of grade three.
	last := func(limit uint64) (uint64, error) { return limit - 1, nil }
	roll, state, err = g.rollWithCostumeFixed(2, 3, fixed, nil, nil, last)
	if err != nil || roll[0] != 55 || state.CostumeGrade4Count != 0 || state.CostumeGrade5Count != 0 {
		t.Fatalf("natural=%v state=%+v err=%v", roll, state, err)
	}
	pool, err := g.gradePool(5)
	if err != nil || len(pool) != 2 || pool[0].ID != 55 || pool[0].Weight != 10 || pool[1].ID != 77 || pool[1].Weight != 5 {
		t.Fatalf("conditioned=%+v err=%v", pool, err)
	}
}

func TestTenDrawGradeFourGuaranteeWorksWithTwoReorderedBranches(t *testing.T) {
	g := RegularGacha{Count: 10, Grades: map[uint64]uint64{99: 3, 66: 4}, Pool: []WeightedCostume{{ID: 99, Weight: 9}, {ID: 66, Weight: 1}}}
	roll, err := g.rollWith(func(uint64) (uint64, error) { return 0, nil })
	if err != nil || roll[9] != 66 {
		t.Fatalf("roll=%v err=%v", roll, err)
	}
}
