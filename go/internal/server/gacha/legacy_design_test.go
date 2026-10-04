package gacha

import "bd2server/internal/server/gamedata"

const (
	moonriseProductGroupID = 1500001
	moonriseProductID      = 9100037
	moonriseTicketType     = 19
	moonriseTicketID       = 450030
	moonriseProductGrant   = "cash-product:1500001:9100037"
	moonriseTicketGrant    = "cash-product-reward:1500001:9100037:0"
	moonriseDrawGrant      = "special-gacha:30011:9100037"
)

const (
	infiniteScheduleGroupID = 30010
	twelvePickGroupID       = 10001
	paidTwelvePickGroupID   = 30011
)

const infiniteGrant = "cash-product:1100001:9100033"

const (
	fixtureInfiniteGachaID        = 9100033
	fixtureInfiniteProductGroupID = 1100001
	fixtureInfiniteProductID      = 9100033
)

func fixtureInfiniteGachaDesign(count int, ids []uint64, characters map[uint64]gamedata.CharacterDesign) (*gamedata.InfiniteGachaDesign, error) {
	return fixtureInfiniteGachaDesignWithRates(count, ids, ids, ids, characters)
}
func fixtureInfiniteGachaDesignWithRates(count int, five, four, three []uint64, characters map[uint64]gamedata.CharacterDesign) (*gamedata.InfiniteGachaDesign, error) {
	d, e := gamedata.NewInfiniteGachaDesignWithRates(count, five, four, three, characters)
	if e == nil {
		d.GroupID = infiniteScheduleGroupID
		d.GachaID = fixtureInfiniteGachaID
		d.ProductGroupID = fixtureInfiniteProductGroupID
		d.ProductID = fixtureInfiniteProductID
	}
	return d, e
}

func fixtureRegularCatalog(gachas map[uint64]gamedata.RegularGacha, characters map[uint64]gamedata.CharacterDesign) (*gamedata.RegularGachaCatalog, error) {
	for id, g := range gachas {
		if len(g.Grades) == 0 {
			g.Grades = map[uint64]uint64{}
			var collect func([]gamedata.WeightedCostume, uint64)
			collect = func(p []gamedata.WeightedCostume, grade uint64) {
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
				collect([]gamedata.WeightedCostume{w}, grade)
			}
			if g.FixedCostumeID != 0 {
				g.Grades[g.FixedCostumeID] = 5
			}
			gachas[id] = g
		}
	}
	return gamedata.NewRegularGachaCatalog(gachas, characters)
}
