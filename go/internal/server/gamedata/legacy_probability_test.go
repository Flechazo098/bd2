package gamedata

import "fmt"

// validateOfficialPickupRates prevents a GameData/schema regression from
// silently changing the published costume pickup rates. The two five-star
// branches are pickup 1.5% plus the ordinary five-star pool 1.5%.
func validateOfficialPickupRates(pool []WeightedCostume) error {
	if len(pool) != 4 {
		return fmt.Errorf("expected four rarity branches, got %d", len(pool))
	}
	want := [...]uint64{150, 150, officialFourStarRate, officialRateScale - officialFiveStarRate - officialFourStarRate}
	for i, item := range pool {
		if item.Weight != want[i] {
			return fmt.Errorf("branch %d weight=%d want=%d", i, item.Weight, want[i])
		}
	}
	return nil
}

func fixtureRegularCatalog(gachas map[uint64]RegularGacha, characters map[uint64]CharacterDesign) (*RegularGachaCatalog, error) {
	for id, g := range gachas {
		if len(g.Grades) == 0 {
			g.Grades = map[uint64]uint64{}
			var collect func([]WeightedCostume, uint64)
			collect = func(p []WeightedCostume, grade uint64) {
				for _, w := range p {
					if w.ID != 0 {
						g.Grades[w.ID] = grade
					} else {
						collect(w.Children, grade)
					}
				}
			}
			for i, w := range g.Pool {
				grade := uint64(5 - i)
				if len(g.Pool) == 4 {
					grade = uint64(6 - i)
					if i == 0 {
						grade = 5
					}
				}
				collect([]WeightedCostume{w}, grade)
			}
			if g.FixedCostumeID != 0 {
				g.Grades[g.FixedCostumeID] = 5
			}
			gachas[id] = g
		}
	}
	return NewRegularGachaCatalog(gachas, characters)
}
