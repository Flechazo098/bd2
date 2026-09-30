package gamedata

import (
	"os"
	"testing"
)

func TestCostumePotentialValidatePrerequisitesAndAggregate(t *testing.T) {
	d := &CostumePotentialDesign{
		Nodes: map[uint64]map[uint64]CostumePotentialNode{100: {
			1: {ID: 1, Costs: []CostumePotentialCost{{Type: 4, Count: 10}}},
			2: {ID: 2, ConditionGrade: 2, Prerequisites: []uint64{1}, Costs: []CostumePotentialCost{{Type: 8, ID: 7, Count: 3}}},
		}},
		CostumeUnique: map[uint64]uint64{100: 9}, CharacterGrade: map[uint64]uint64{200: 2}, CharacterUnique: map[uint64]uint64{200: 9},
	}
	costs, err := d.Validate(100, 200, 0, nil, []uint64{1, 2})
	if err != nil || len(costs) != 2 {
		t.Fatalf("valid chain costs=%+v err=%v", costs, err)
	}
	if _, err := d.Validate(100, 200, 0, nil, []uint64{2}); err == nil {
		t.Fatal("missing prerequisite accepted")
	}
	if _, err := d.Validate(100, 201, 0, nil, []uint64{1}); err == nil {
		t.Fatal("wrong character accepted")
	}
}

func TestCostumePotentialAgainstInstalledVersion23413(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	design, err := LoadCostumePotentialDesign(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]uint64, 0, len(design.Nodes[65103]))
	for id := range design.Nodes[65103] {
		nodes = append(nodes, id)
	}
	costs, err := design.Validate(65103, 6514, 0, nil, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 17 || len(costs) < 17 {
		t.Fatalf("installed costume 65103 nodes=%d costs=%d", len(nodes), len(costs))
	}
	questCosts, err := design.Validate(3501, 354, 0, nil, []uint64{1})
	if err != nil || len(questCosts) != 1 || questCosts[0].Type != 4 || questCosts[0].Count == 0 {
		t.Fatalf("installed quest reward costume 3501 first node costs=%+v err=%v", questCosts, err)
	}
}
