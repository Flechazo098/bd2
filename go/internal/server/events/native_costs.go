package events

import (
	"fmt"
	"math"

	"bd2server/internal/server/gamedata"
)

// Native shops' FreeJewelry prices use total diamonds: free first, then paid.
// Cash conversion policy and explicitly paid-only prices keep their own rules.
func (e *Economy) NativePurchaseCosts(cost gamedata.Reward) ([]gamedata.Reward, error) {
	if cost.Count == 0 || cost.Count > math.MaxInt32 {
		return nil, fmt.Errorf("events: invalid native purchase cost")
	}
	if cost.Type != 3 {
		return []gamedata.Reward{cost}, nil
	}
	if cost.ID != 0 {
		return nil, fmt.Errorf("events: diamond cost has an item ID")
	}
	balance := e.wallet.Snapshot()
	free := min(cost.Count, balance.FreeJewelry)
	paid := cost.Count - free
	if paid > balance.Jewelry {
		return nil, fmt.Errorf("events: insufficient total diamonds")
	}
	var costs []gamedata.Reward
	if free > 0 {
		costs = append(costs, gamedata.Reward{Type: 3, Count: free})
	}
	if paid > 0 {
		costs = append(costs, gamedata.Reward{Type: 2, Count: paid})
	}
	return costs, nil
}
