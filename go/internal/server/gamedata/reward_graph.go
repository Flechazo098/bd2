package gamedata

import (
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
)

// RewardGraph resolves random-box nodes; costume/equipment leaves retain
// their design IDs for the owning grant domain.
type RewardGraph struct {
	mu     sync.Mutex
	boxes  map[uint64]uint64
	direct map[uint64]bool
	groups map[uint64][]byte
	sample func(uint64) (uint64, error)
}

func LoadRewardGraph(root, version string) (*RewardGraph, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	g := &RewardGraph{boxes: map[uint64]uint64{}, direct: map[uint64]bool{}, groups: map[uint64][]byte{}}
	g.sample = func(n uint64) (uint64, error) {
		if n == 0 {
			return 0, fmt.Errorf("gamedata: empty reward pool")
		}
		v, e := rand.Int(rand.Reader, new(big.Int).SetUint64(n))
		if e != nil {
			return 0, e
		}
		return v.Uint64(), nil
	}
	rows, e := db.Query("SELECT id,ProtoBuf FROM RandomBoxTable")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			rows.Close()
			return nil, e
		}
		gid, e := optionalScalar(raw, 9)
		if e != nil {
			rows.Close()
			return nil, e
		}
		drop, err := optionalScalar(raw, 1)
		if err != nil {
			rows.Close()
			return nil, err
		}
		g.direct[id] = drop == 1
		g.boxes[id] = gid
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = db.Query("SELECT id,ProtoBuf FROM RewardGroupTable")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			rows.Close()
			return nil, e
		}
		g.groups[id] = append([]byte(nil), raw...)
	}
	e = rows.Err()
	rows.Close()
	return g, e
}
func (g *RewardGraph) SetSampler(f func(uint64) (uint64, error)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sample = f
}
func (g *RewardGraph) Resolve(rewards []BattleReward) ([]BattleReward, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	budget := uint64(100000)
	totals := map[[2]uint64]uint64{}
	seen := map[uint64]bool{}
	var emit func(BattleReward) error
	emit = func(r BattleReward) error {
		if r.Count == 0 || r.Count > math.MaxInt32 || r.Type == 0 {
			return fmt.Errorf("gamedata: invalid reward graph quantity/type")
		}
		if budget == 0 {
			return fmt.Errorf("gamedata: reward graph operation limit")
		}
		budget--
		if r.Type != 9 {
			k := [2]uint64{r.Type, r.ID}
			if totals[k] > math.MaxInt32-r.Count {
				return fmt.Errorf("gamedata: reward graph overflow")
			}
			totals[k] += r.Count
			return nil
		}
		if seen[r.ID] || len(seen) >= 32 {
			return fmt.Errorf("gamedata: reward graph cycle")
		}
		gid, ok := g.boxes[r.ID]
		if !ok {
			return fmt.Errorf("gamedata: unknown random box %d", r.ID)
		}
		raw, ok := g.groups[gid]
		if !ok {
			return fmt.Errorf("gamedata: missing reward group %d", gid)
		}
		drop, e := optionalScalar(raw, 2)
		if e != nil {
			return e
		}
		count, e := optionalScalar(raw, 1)
		if e != nil {
			return e
		}
		children, e := eventGameRewards(raw, 6, 5, 4)
		if e != nil || len(children) == 0 {
			return fmt.Errorf("gamedata: invalid random box reward group %d", gid)
		}
		weights, e := packedInts(raw, 8)
		if e != nil {
			return e
		}
		if drop > 1 || len(weights) != len(children) || drop == 0 && count == 0 {
			return fmt.Errorf("gamedata: invalid reward group selection mode")
		}
		seen[r.ID] = true
		defer delete(seen, r.ID)
		for n := uint64(0); n < r.Count; n++ {
			if budget == 0 {
				return fmt.Errorf("gamedata: reward graph operation limit")
			}
			budget--
			if drop == 1 {
				// Local server policy: direct components independently roll their GameData percentage.
				for i, child := range children {
					if weights[i] > 100 {
						return fmt.Errorf("gamedata: direct reward percent exceeds 100")
					}
					x, e := g.sample(100)
					if e != nil {
						return e
					}
					if x >= 100 {
						return fmt.Errorf("gamedata: sampler out of range")
					}
					if x >= weights[i] {
						continue
					}
					if e = emit(child); e != nil {
						return e
					}
				}
				continue
			}
			sum := uint64(0)
			for _, w := range weights {
				if sum > math.MaxUint64-w {
					return fmt.Errorf("gamedata: reward graph weight overflow")
				}
				sum += w
			}
			if sum == 0 || g.sample == nil {
				return fmt.Errorf("gamedata: empty weighted reward pool")
			}
			for i := uint64(0); i < count; i++ {
				if budget == 0 {
					return fmt.Errorf("gamedata: reward graph operation limit")
				}
				x, e := g.sample(sum)
				if e != nil {
					return e
				}
				if x >= sum {
					return fmt.Errorf("gamedata: random result outside reward pool")
				}
				for j, w := range weights {
					if x < w {
						if e = emit(children[j]); e != nil {
							return e
						}
						break
					}
					x -= w
				}
			}
		}
		return nil
	}
	for _, r := range rewards {
		if e := emit(r); e != nil {
			return nil, e
		}
	}
	var out []BattleReward
	for k, v := range totals {
		out = append(out, BattleReward{Type: k[0], ID: k[1], Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ResolveGranted preserves manually opened boxes; only RbdDirect boxes expand.
func (g *RewardGraph) ResolveGranted(rewards []BattleReward) ([]BattleReward, error) {
	var out []BattleReward
	for _, r := range rewards {
		if r.Type == 9 && !g.direct[r.ID] {
			out = append(out, r)
			continue
		}
		expanded, e := g.Resolve([]BattleReward{r})
		if e != nil {
			return nil, e
		}
		out = append(out, expanded...)
	}
	return out, nil
}
