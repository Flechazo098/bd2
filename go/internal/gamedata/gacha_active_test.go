package gamedata

import (
	"os"
	"testing"
)

func TestSingleSidedCostumeFixedThresholdsDoNotTriggerZeroSide(t *testing.T) {
	pool := []WeightedCostume{
		{Weight: 150, ID: 5001}, {Weight: 150, ID: 5002},
		{Weight: 1400, ID: 4001}, {Weight: 8300, ID: 3001},
	}
	gacha := RegularGacha{ID: 1, Count: 1, PriceType: 3, Price: 200, Pool: pool}
	chooseThree := func(limit uint64) (uint64, error) {
		if limit == officialRateScale {
			return limit - 1, nil
		}
		return 0, nil
	}
	roll, state, err := gacha.rollWithCostumeFixed(0, 0, GachaFixedDesign{ID: 3, CostumeGrade5Count: 10}, nil, nil, chooseThree)
	if err != nil || roll[0] != 3001 || state.CostumeGrade4Sort != -1 || state.CostumeGrade5Sort != -1 {
		t.Fatalf("five-only fixed roll=%v state=%+v err=%v", roll, state, err)
	}
	roll, state, err = gacha.rollWithCostumeFixed(9, 500, GachaFixedDesign{ID: 5, CostumeGrade4Count: 10, ResetOnMatchingGrade: true}, nil, nil, chooseThree)
	if err != nil || roll[0] != 4001 || state.CostumeGrade4Sort != 0 || state.CostumeGrade5Sort != -1 {
		t.Fatalf("four-only fixed roll=%v state=%+v err=%v", roll, state, err)
	}
}

func TestCompositeCostumeRewardGroupExecutesAllChildren(t *testing.T) {
	program := &CostumeRewardGroup{ID: 1, DropCount: 1, DropType: 1, Entries: []CostumeRewardEntry{
		{ItemType: 11, ItemID: 5001, Count: 1, Weight: 1},
		{ItemType: 9, ItemID: 2, Count: 1, Weight: 1, Group: &CostumeRewardGroup{ID: 2, DropCount: 9, Entries: []CostumeRewardEntry{
			{ItemType: 11, ItemID: 4001, Count: 1, Weight: 1400},
			{ItemType: 11, ItemID: 3001, Count: 1, Weight: 8300},
		}}},
	}}
	if count, err := costumeRewardCount(program); err != nil || count != 10 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	gacha := RegularGacha{ID: 1, Count: 10, PriceType: 2, Price: 500, RewardGroup: program}
	roll, err := gacha.rollRewardGroup(func(limit uint64) (uint64, error) { return 0, nil })
	if err != nil || len(roll) != 10 || roll[0] != 5001 {
		t.Fatalf("roll=%v err=%v", roll, err)
	}
	for i := 1; i < len(roll); i++ {
		if roll[i] != 4001 {
			t.Fatalf("roll[%d]=%d", i, roll[i])
		}
	}
}

func TestMoonriseSpecialSelectionUsesThreeChoicesAndGameDataRemainder(t *testing.T) {
	choices := make([]CostumeRewardEntry, 12)
	selected := make([]uint64, 12)
	for i := range choices {
		selected[i] = uint64(5001 + i)
		choices[i] = CostumeRewardEntry{ItemType: 11, ItemID: selected[i], Count: 1, Weight: 1}
	}
	gacha := RegularGacha{ID: 9100037, Count: 10, PriceType: 19, PriceID: 450030, Price: 1, RewardGroup: &CostumeRewardGroup{
		ID: 9100037, DropCount: 1, DropType: 1, Entries: []CostumeRewardEntry{
			{ItemType: 9, ItemID: 9100038, Count: 1, Weight: 1, Group: &CostumeRewardGroup{ID: 9100038, DropCount: 3, Entries: choices}},
			{ItemType: 9, ItemID: 9100039, Count: 1, Weight: 1, Group: &CostumeRewardGroup{ID: 9100039, DropCount: 7, Entries: []CostumeRewardEntry{
				{ItemType: 11, ItemID: 4001, Count: 1, Weight: 1443},
				{ItemType: 11, ItemID: 3001, Count: 1, Weight: 8557},
			}}},
		},
	}}
	draws := []uint64{0, 1, 2, 0, 9999, 0, 9999, 0, 9999, 0}
	roll, err := gacha.rollSpecialSelection(selected, func(limit uint64) (uint64, error) {
		value := draws[0]
		draws = draws[1:]
		return value % limit, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(roll) != 10 || roll[0] != selected[0] || roll[1] != selected[1] || roll[2] != selected[2] {
		t.Fatalf("selection results=%v", roll)
	}
	for _, id := range roll[3:] {
		if id != 4001 && id != 3001 {
			t.Fatalf("remainder contains non-GameData costume %d: %v", id, roll)
		}
	}
}

func TestAddAndQueryStepUpDesign(t *testing.T) {
	characters := map[uint64]CharacterDesign{5001: {ID: 500, HP: 100}}
	catalog, err := NewRegularGachaCatalog(map[uint64]RegularGacha{
		10: {ID: 10, Count: 1, PriceType: 2, Price: 1, Pool: []WeightedCostume{{ID: 5001, Weight: 1}}},
	}, characters)
	if err != nil {
		t.Fatal(err)
	}
	design := GachaStepUpDesign{ID: 30, Steps: []GachaStepDesign{{Step: 1, GroupID: 20122, GachaID: 10, FixedID: 5, IsDisplayFixedItem: 1}}}
	if err := catalog.AddStepUpDesign(design); err != nil {
		t.Fatal(err)
	}
	step, ok := catalog.StepForGacha(10)
	if !ok || step.GroupID != 20122 || step.Step != 1 {
		t.Fatalf("step=%+v ok=%v", step, ok)
	}
	group, ok := catalog.StepUp(30)
	if !ok || len(group.Steps) != 1 || len(catalog.StepUps()) != 1 {
		t.Fatalf("group=%+v ok=%v", group, ok)
	}
}

func TestActiveGachaAgainstInstalledVersion23510(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	regular, equipment, err := LoadActiveGachaForSchedules(root, "20260923193640",
		[]uint64{166, 30010, 30011, 206, 205, 208, 153, 72, 71, 207, 1009}, []uint64{29, 30})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint64{101, 10100113, 11000113, 10100145, 11000145, 10100040, 11000040, 10100146, 11000146, 31, 9100037, 8100118, 8100119, 8100120, 8100121, 8100122, 8100123, 8100124, 8100125} {
		if _, ok := regular.Gacha(id); !ok {
			t.Errorf("costume gacha %d missing", id)
		}
	}
	paid, ok := regular.Gacha(9100037)
	if !ok || paid.PriceType != 19 || paid.PriceID != 450030 || paid.Price != 1 || paid.RewardGroup == nil {
		t.Fatalf("paid selection gacha=%+v ok=%v", paid, ok)
	}
	if got := regular.SpecialSelectionCount(9100037); got != 3 || len(regular.SpecialSelectionIDs(9100037)) != 129 {
		t.Fatalf("paid selection guaranteed=%d eligible=%d", got, len(regular.SpecialSelectionIDs(9100037)))
	}
	one, ok := regular.Gacha(10100113)
	if !ok || one.Count != 1 || one.FreeCountDay != 1 || one.DailyPayGachaCount != 1 || one.DailyPayGachaPriceCount != 90 || one.Price != 200 {
		t.Fatalf("daily single gacha=%+v ok=%v", one, ok)
	}
	moonriseGroup, ok := regular.Group(30011)
	if !ok || moonriseGroup.BuyLimitCount != 10 || moonriseGroup.CashProductGroupID != 1500001 || moonriseGroup.CashProductID != 9100037 || moonriseGroup.SelectCount != 12 || moonriseGroup.SelectionChoiceRate != 100 {
		t.Fatalf("moonrise group=%+v ok=%v", moonriseGroup, ok)
	}
	permanentSelection, ok := regular.Group(10001)
	if !ok || permanentSelection.TenTimeGachaID != 101 || permanentSelection.SelectCount != 12 || permanentSelection.GachaSubType != 1 {
		t.Fatalf("permanent selection group=%+v ok=%v", permanentSelection, ok)
	}
	newbie, ok := regular.Group(1009)
	if !ok || newbie.TenTimeGachaID != 31 || newbie.BuyLimitCount != 30 || newbie.SelectCount != 3 || newbie.PointCount != 0 || newbie.FixedID != 3 || !newbie.UseSelectionOnlyFixedApply {
		t.Fatalf("newbie group=%+v ok=%v", newbie, ok)
	}
	for _, id := range []uint64{8100121, 8100125} {
		gacha, ok := regular.Gacha(id)
		if !ok || gacha.FixedCostumeID == 0 || gacha.RewardGroup == nil || len(gacha.Pool) != 3 {
			t.Fatalf("fixed step gacha %d=%+v ok=%v", id, gacha, ok)
		}
	}
	for _, fixedID := range []uint64{1, 3, 5} {
		if _, ok := regular.Fixed(fixedID); !ok {
			t.Errorf("fixed %d missing", fixedID)
		}
	}
	for _, groupID := range []uint64{29, 30} {
		stepUp, ok := regular.StepUp(groupID)
		if !ok || len(stepUp.Steps) != 4 {
			t.Errorf("step-up %d=%+v ok=%v", groupID, stepUp, ok)
		}
	}
	for _, id := range []uint64{20100082, 21000082, 20100034, 21000034, 20100070, 21000070, 20100083, 21000083} {
		if _, ok := equipment.Gacha(id); !ok {
			t.Errorf("equipment gacha %d missing", id)
		}
	}
}

func TestLegacyRegularGachaLoaderAgainstInstalledVersion23413(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	catalog, err := LoadRegularCostumeGacha(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint64{100, 101, 10100084, 11000084, 10100145, 11000145, 10100072, 11000072, 8100118, 8100119, 8100120, 8100121} {
		if _, ok := catalog.Gacha(id); !ok {
			t.Errorf("legacy gacha %d missing", id)
		}
	}
}
