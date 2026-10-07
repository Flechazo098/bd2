package gamedata

import (
	"database/sql"
	"fmt"
)

// Cash product tickets are the deterministic CashProductTable -> RandomBoxTable
// -> RewardGroupTable grant. Their IDs and quantities are version facts.
func loadGachaCashRewards(db *sql.DB, group GachaGroupDesign) ([]BattleReward, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CashProductTable WHERE GroupId=? AND id=? AND saleGroup=?", group.CashProductGroupID, group.CashProductID, group.CashSalesGroup).Scan(&raw); err != nil {
		return nil, err
	}
	box, err := optionalScalar(raw, 14)
	if err != nil || box == 0 {
		return nil, fmt.Errorf("gamedata: gacha cash product has no random box")
	}
	if err := db.QueryRow("SELECT ProtoBuf FROM RandomBoxTable WHERE id=?", box).Scan(&raw); err != nil {
		return nil, err
	}
	rewardID, err := optionalScalar(raw, 9)
	if err != nil || rewardID == 0 {
		return nil, fmt.Errorf("gamedata: gacha cash box has no reward group")
	}
	return deterministicGachaCashRewards(db, rewardID, map[uint64]bool{})
}

func deterministicGachaCashRewards(db *sql.DB, id uint64, visiting map[uint64]bool) ([]BattleReward, error) {
	if visiting[id] {
		return nil, fmt.Errorf("gamedata: cyclic cash reward group %d", id)
	}
	visiting[id] = true
	defer delete(visiting, id)
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", id).Scan(&raw); err != nil {
		return nil, err
	}
	dropType, err := optionalScalar(raw, 2)
	if err != nil {
		return nil, err
	}
	ids, err := packedInts(raw, 5)
	if err != nil {
		return nil, err
	}
	types, err := packedInts(raw, 6)
	if err != nil {
		return nil, err
	}
	counts, err := packedInts(raw, 4)
	if err != nil {
		return nil, err
	}
	ratios, err := packedInts(raw, 8)
	if err != nil || len(ratios) != len(ids) {
		return nil, fmt.Errorf("gamedata: malformed cash reward ratio")
	}
	for _, ratio := range ratios {
		if dropType == 0 && ratio == 0 || dropType == 1 && ratio != 100 {
			return nil, fmt.Errorf("gamedata: cash reward is not deterministic")
		}
	}
	if len(ids) == 0 || len(ids) != len(types) || len(ids) != len(counts) || dropType > 1 || dropType == 0 && len(ids) != 1 {
		return nil, fmt.Errorf("gamedata: cash reward %d is not deterministic", id)
	}
	dropCount, err := optionalScalar(raw, 1)
	if err != nil || dropType == 0 && dropCount == 0 {
		return nil, fmt.Errorf("gamedata: cash reward has invalid drop count")
	}
	if dropType == 0 {
		for i := range counts {
			if counts[i] > ^uint64(0)/dropCount {
				return nil, fmt.Errorf("gamedata: cash reward count overflow")
			}
			counts[i] *= dropCount
		}
	}
	var out []BattleReward
	for i, itemID := range ids {
		if counts[i] == 0 {
			return nil, fmt.Errorf("gamedata: cash reward %d has zero count", id)
		}
		if types[i] == 9 {
			children, err := deterministicGachaCashRewards(db, itemID, visiting)
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				if child.Count > ^uint64(0)/counts[i] {
					return nil, fmt.Errorf("gamedata: cash reward overflow")
				}
				child.Count *= counts[i]
				out = append(out, child)
			}
		} else {
			if (!itemDBInfoRewardType(types[i]) && types[i] != 19) || itemID == 0 {
				return nil, fmt.Errorf("gamedata: unsupported cash reward type %d", types[i])
			}
			out = append(out, BattleReward{Type: types[i], ID: itemID, Count: counts[i]})
		}
	}
	return out, nil
}
