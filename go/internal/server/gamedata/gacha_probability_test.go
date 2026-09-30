package gamedata

import "testing"

func TestInfiniteGachaOfficialRateBoundariesAndFiveStarGuarantee(t *testing.T) {
	characters := map[uint64]CharacterDesign{
		5001: {ID: 500, HP: 100},
		4001: {ID: 400, HP: 100},
		3001: {ID: 300, HP: 100},
	}
	design, err := NewInfiniteGachaDesignWithRates(6, []uint64{5001}, []uint64{4001}, []uint64{3001}, characters)
	if err != nil {
		t.Fatal(err)
	}
	// The first five pairs are rate roll + pool index. They cover both
	// boundaries of 3%, the 14% interval and the start of the 83% interval.
	// The final value chooses the guaranteed five-star slot.
	draws := []uint64{0, 0, 299, 0, 300, 0, 1699, 0, 1700, 0, 0}
	position := 0
	roll, err := design.rollWith(func(limit uint64) (uint64, error) {
		if position >= len(draws) {
			t.Fatal("unexpected extra random draw")
		}
		value := draws[position]
		position++
		if value >= limit {
			t.Fatalf("draw %d exceeds limit %d", value, limit)
		}
		return value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{5001, 5001, 4001, 4001, 3001, 5001}
	for i := range want {
		if roll[i] != want[i] {
			t.Fatalf("slot %d=%d want=%d; roll=%v", i, roll[i], want[i], roll)
		}
	}
	if position != len(draws) {
		t.Fatalf("used %d random draws, want %d", position, len(draws))
	}
}

func TestOfficialPickupRateDefinition(t *testing.T) {
	pool := []WeightedCostume{
		{Weight: 150, ID: 1},
		{Weight: 150, ID: 2},
		{Weight: 1400, ID: 3},
		{Weight: 8300, ID: 4},
	}
	if err := validateOfficialPickupRates(pool); err != nil {
		t.Fatal(err)
	}
	pool[0].Weight++
	if err := validateOfficialPickupRates(pool); err == nil {
		t.Fatal("accepted a pickup rate different from official GameData")
	}
}

func TestTenPullGuaranteesGradeFourWhenAllNormalRollsAreGradeThree(t *testing.T) {
	gacha := RegularGacha{Count: 10, Pool: []WeightedCostume{
		{Weight: 150, ID: 5001}, {Weight: 150, ID: 5002}, {Weight: 1400, ID: 4001}, {Weight: 8300, ID: 3001},
	}}
	roll, err := gacha.rollWith(func(limit uint64) (uint64, error) {
		if limit == officialRateScale {
			return 9999, nil
		}
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		if roll[i] != 3001 {
			t.Fatalf("slot %d=%d want grade-3", i, roll[i])
		}
	}
	if roll[9] != 4001 {
		t.Fatalf("guaranteed slot=%d want grade-4; roll=%v", roll[9], roll)
	}
}

func TestFixedPickupTenPullAlwaysStartsWithPickupCostume(t *testing.T) {
	gacha := RegularGacha{Count: 10, FixedCostumeID: 21201, Pool: []WeightedCostume{
		{Weight: 300, ID: 5002}, {Weight: 1400, ID: 4001}, {Weight: 8300, ID: 3001},
	}}
	roll, err := gacha.rollWith(func(limit uint64) (uint64, error) { return limit - 1, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(roll) != 10 || roll[0] != 21201 {
		t.Fatalf("roll=%v", roll)
	}
	for i := 1; i < 10; i++ {
		if roll[i] != 3001 {
			t.Fatalf("slot %d=%d want grade-3", i, roll[i])
		}
	}
}
