package gamedata

import (
	"fmt"
	"math"
)

// CashRewardResolver gives cash combination groups their storefront meaning:
// ratio 1 and ratio 100 both describe guaranteed components. Weighted groups
// keep the shared reward graph sampler, and manually opened boxes stay items.
type CashRewardResolver struct {
	graph  *RewardGraph
	boxes  map[uint64]uint64
	direct map[uint64]bool
	groups map[uint64][]byte
}

func LoadCashRewardResolver(root, version string, shared ...*RewardGraph) (*CashRewardResolver, error) {
	var graph *RewardGraph
	if len(shared) > 0 {
		graph = shared[0]
	}
	if graph == nil {
		var err error
		graph, err = LoadRewardGraph(root, version)
		if err != nil {
			return nil, err
		}
	}
	// The shared immutable design maps are initialized completely at startup.
	return &CashRewardResolver{graph: graph, boxes: graph.boxes, direct: graph.direct, groups: graph.groups}, nil
}
func (c *CashRewardResolver) ResolveGranted(rewards []BattleReward) ([]BattleReward, error) {
	budget := uint64(100000)
	visiting := map[uint64]bool{}
	var out []BattleReward
	var emit func(BattleReward, bool) error
	emit = func(r BattleReward, force bool) error {
		if budget == 0 || r.Count == 0 || r.Count > math.MaxInt32 || r.Type == 0 {
			return fmt.Errorf("gamedata: invalid cash reward/budget")
		}
		budget--
		if r.Type != 9 || !force && !c.direct[r.ID] {
			out = append(out, r)
			return nil
		}
		if visiting[r.ID] || len(visiting) >= 32 {
			return fmt.Errorf("gamedata: cash reward cycle")
		}
		gid, ok := c.boxes[r.ID]
		if !ok {
			return fmt.Errorf("gamedata: unknown cash random box %d", r.ID)
		}
		raw, ok := c.groups[gid]
		if !ok {
			return fmt.Errorf("gamedata: unknown cash reward group %d", gid)
		}
		drop, err := optionalScalar(raw, 2)
		if err != nil {
			return err
		}
		if drop == 0 {
			selected, err := c.graph.Resolve([]BattleReward{r})
			if err != nil {
				return err
			}
			out = append(out, selected...)
			return nil
		}
		if drop != 1 {
			return fmt.Errorf("gamedata: unsupported cash reward mode %d", drop)
		}
		children, err := eventGameRewards(raw, 6, 5, 4)
		if err != nil || len(children) == 0 {
			return fmt.Errorf("gamedata: malformed cash combination")
		}
		weights, err := packedInts(raw, 8)
		if err != nil || len(weights) != len(children) {
			return fmt.Errorf("gamedata: malformed cash combination ratios")
		}
		visiting[r.ID] = true
		defer delete(visiting, r.ID)
		for i, child := range children {
			if weights[i] != 1 && weights[i] != 100 {
				return fmt.Errorf("gamedata: cash combination ratio %d is not guaranteed", weights[i])
			}
			if child.Count > math.MaxInt32/r.Count {
				return fmt.Errorf("gamedata: cash reward overflow")
			}
			child.Count *= r.Count
			if err = emit(child, false); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range rewards {
		if err := emit(r, true); err != nil {
			return nil, err
		}
	}
	return out, nil
}
