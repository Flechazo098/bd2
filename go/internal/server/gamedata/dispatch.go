package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
)

type DispatchDesign struct {
	Group, ID, Pack, Difficulty, TypeGroup, TypeID, AP, ClearTime, Growth uint64
	GroundID                                                              uint64
	Rewards                                                               []BattleReward
	boxes                                                                 map[uint64]*dispatchGroup
}
type dispatchEntry struct {
	Reward BattleReward
	Weight uint64
	Child  *dispatchGroup
}
type dispatchGroup struct {
	Drop, Count uint64
	Entries     []dispatchEntry
}

func LoadDispatchDesign(root, version string, group, id uint64) (*DispatchDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	var raw []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM HuntDispatchTable WHERE groupId=? AND id=?", group, id).Scan(&raw); err != nil {
		return nil, err
	}
	d := &DispatchDesign{boxes: map[uint64]*dispatchGroup{}}
	for n, p := range map[int]*uint64{1: &d.AP, 4: &d.ClearTime, 5: &d.Difficulty, 7: &d.Group, 8: &d.ID, 10: &d.Pack, 12: &d.Growth, 13: &d.TypeGroup, 14: &d.TypeID} {
		*p, err = optionalScalar(raw, n)
		if err != nil {
			return nil, err
		}
	}
	if d.AP == 0 || d.Pack == 0 || d.Group != group || d.ID != id {
		return nil, fmt.Errorf("gamedata: invalid dispatch")
	}
	pdb, closePack, err := openPackDatabase(root, version, int(d.Pack))
	if err != nil {
		return nil, err
	}
	defer closePack()
	var ids []uint64
	var boss uint64
	if d.TypeGroup == 0 {
		if err = pdb.QueryRow("SELECT ProtoBuf FROM HuntingGroundTable WHERE difficulty=?", d.Difficulty).Scan(&raw); err != nil {
			return nil, err
		}
		ids, err = packedInts(raw, 5)
		boss, _ = optionalScalar(raw, 1)
		d.GroundID, _ = optionalScalar(raw, 3)
	} else {
		if err = db.QueryRow("SELECT ProtoBuf FROM SkyWayFieldTable WHERE groupId=? AND id=?", d.TypeGroup, d.TypeID).Scan(&raw); err != nil {
			return nil, err
		}
		ids, err = packedInts(raw, 15)
		boss, _ = optionalScalar(raw, 4)
	}
	if err != nil {
		return nil, err
	}
	ids = append(ids, boss)
	for _, monster := range ids {
		if err = pdb.QueryRow("SELECT ProtoBuf FROM FieldMonsterTable WHERE id=?", monster).Scan(&raw); err != nil {
			return nil, err
		}
		decks, e := packedInts(raw, 2)
		if e != nil || len(decks) == 0 {
			return nil, fmt.Errorf("gamedata: dispatch monster has no deck")
		}
		if err = pdb.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", decks[0]).Scan(&raw); err != nil {
			return nil, err
		}
		types, e := packedInts(raw, 36)
		if e != nil {
			return nil, e
		}
		rewardIDs, e := packedInts(raw, 34)
		if e != nil {
			return nil, e
		}
		counts, e := packedInts(raw, 33)
		if e != nil {
			return nil, e
		}
		if len(types) != len(rewardIDs) || len(types) != len(counts) {
			return nil, fmt.Errorf("gamedata: dispatch reward arrays mismatch")
		}
		rs, e := parallelRewards(raw, 36, 34, 33)
		if e != nil {
			return nil, e
		}
		for _, r := range rs {
			d.Rewards = append(d.Rewards, BattleReward{r.Type, r.ID, r.Count})
			if r.Type == 9 {
				g, e := loadDispatchBox(db, r.ID, map[uint64]bool{})
				if e != nil {
					return nil, e
				}
				d.boxes[r.ID] = g
			}
		}
	}
	return d, nil
}
func loadDispatchBox(db *sql.DB, id uint64, seen map[uint64]bool) (*dispatchGroup, error) {
	if seen[id] || len(seen) > 32 {
		return nil, fmt.Errorf("gamedata: dispatch reward cycle")
	}
	seen[id] = true
	defer delete(seen, id)
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RandomBoxTable WHERE id=?", id).Scan(&raw); err != nil {
		return nil, err
	}
	gid, err := optionalScalar(raw, 9)
	if err != nil {
		return nil, err
	}
	if err = db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", gid).Scan(&raw); err != nil {
		return nil, err
	}
	g := &dispatchGroup{}
	g.Drop, _ = optionalScalar(raw, 2)
	g.Count, _ = optionalScalar(raw, 1)
	fields := map[int][]uint64{}
	for _, n := range []int{4, 5, 6, 8} {
		fields[n], err = packedInts(raw, n)
		if err != nil {
			return nil, err
		}
	}
	if len(fields[5]) == 0 || len(fields[4]) != len(fields[5]) || len(fields[6]) != len(fields[5]) || len(fields[8]) != len(fields[5]) || g.Drop > 1 || (g.Drop == 0 && g.Count == 0) {
		return nil, fmt.Errorf("gamedata: malformed dispatch reward group %d", gid)
	}
	for i, item := range fields[5] {
		e := dispatchEntry{Reward: BattleReward{fields[6][i], item, fields[4][i]}, Weight: fields[8][i]}
		if e.Reward.Count == 0 {
			return nil, fmt.Errorf("gamedata: empty dispatch reward")
		}
		if e.Reward.Type == 9 {
			e.Child, err = loadDispatchBox(db, item, seen)
			if err != nil {
				return nil, err
			}
		}
		g.Entries = append(g.Entries, e)
	}
	return g, nil
}

// Roll uses the local server's random source; GameData supplies every weight and quantity.
func (d *DispatchDesign) Roll(count uint64, draw func(uint64) (uint64, error)) ([]BattleReward, error) {
	if d == nil || count == 0 || count > 20 {
		return nil, fmt.Errorf("gamedata: invalid dispatch count")
	}
	var budget uint64 = 100000
	var out []BattleReward
	var run func(*dispatchGroup) error
	emit := func(e dispatchEntry) error {
		if e.Child != nil {
			for i := uint64(0); i < e.Reward.Count; i++ {
				if err := run(e.Child); err != nil {
					return err
				}
			}
		} else {
			if len(out) >= 100000 {
				return fmt.Errorf("gamedata: dispatch reward count exceeds operation limit")
			}
			out = append(out, e.Reward)
		}
		return nil
	}
	run = func(g *dispatchGroup) error {
		if g == nil || budget == 0 {
			return fmt.Errorf("gamedata: dispatch reward program exceeds operation limit")
		}
		budget--
		if g.Drop == 1 {
			for _, e := range g.Entries {
				if e.Weight > 100 {
					return fmt.Errorf("gamedata: direct dispatch percent exceeds 100")
				}
				v, err := draw(100)
				if err != nil {
					return err
				}
				if v >= 100 {
					return fmt.Errorf("gamedata: sampler out of range")
				}
				if v >= e.Weight {
					continue
				}
				if err := emit(e); err != nil {
					return err
				}
			}
			return nil
		}
		var sum uint64
		for _, e := range g.Entries {
			if e.Weight == 0 || math.MaxUint64-sum < e.Weight {
				return fmt.Errorf("gamedata: invalid dispatch weight")
			}
			sum += e.Weight
		}
		for i := uint64(0); i < g.Count; i++ {
			v, err := draw(sum)
			if err != nil {
				return err
			}
			if v >= sum {
				return fmt.Errorf("gamedata: random out of range")
			}
			for _, e := range g.Entries {
				if v < e.Weight {
					if err := emit(e); err != nil {
						return err
					}
					break
				}
				v -= e.Weight
			}
		}
		return nil
	}
	for i := uint64(0); i < count; i++ {
		for _, r := range d.Rewards {
			if r.Type == 9 {
				for n := uint64(0); n < r.Count; n++ {
					if err := run(d.boxes[r.ID]); err != nil {
						return nil, err
					}
				}
			} else {
				out = append(out, r)
			}
		}
	}
	aggregate := map[[2]uint64]uint64{}
	for _, r := range out {
		k := [2]uint64{r.Type, r.ID}
		if r.Count > math.MaxInt32-aggregate[k] {
			return nil, fmt.Errorf("gamedata: dispatch reward overflow")
		}
		aggregate[k] += r.Count
	}
	out = nil
	for k, n := range aggregate {
		out = append(out, BattleReward{k[0], k[1], n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
