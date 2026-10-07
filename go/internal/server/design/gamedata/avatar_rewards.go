package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

// Avatar sets are design bundles: ownership is checked on each member by the
// client, rather than on a synthetic AvatarSet ItemDBInfo.
type AvatarRewardDesign struct {
	Sets  map[uint64][]BattleReward
	Items map[uint64]map[uint64]bool
}

func LoadAvatarRewardDesign(root, version string) (*AvatarRewardDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	return loadAvatarRewardDesign(db)
}
func loadAvatarRewardDesign(db *sql.DB) (*AvatarRewardDesign, error) {
	d := &AvatarRewardDesign{Sets: map[uint64][]BattleReward{}, Items: map[uint64]map[uint64]bool{49: {}, 50: {}, 61: {}}}
	for _, table := range []struct {
		name  string
		typ   uint64
		field int
	}{{"AvatarItemTable", 49, 3}, {"AvatarMotionTable", 50, 5}, {"AvatarCharTable", 61, 6}} {
		if err := readCashMetadata(db, table.name, func(raw []byte) error {
			id, err := optionalScalar(raw, table.field)
			if err != nil {
				return err
			}
			if id == 0 {
				return fmt.Errorf("gamedata: invalid avatar member")
			}
			d.Items[table.typ][id] = true
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err := readCashMetadata(db, "AvatarSetTable", func(raw []byte) error {
		id, err := optionalScalar(raw, 6)
		if err != nil {
			return err
		}
		members, err := eventGameRewards(raw, 5, 4, 3)
		if err != nil || id == 0 || len(members) == 0 {
			return fmt.Errorf("gamedata: invalid avatar set")
		}
		for _, member := range members {
			if !d.Items[member.Type][member.ID] || member.Count == 0 || member.Count > math.MaxInt32 {
				return fmt.Errorf("gamedata: avatar set %d has invalid member %d:%d", id, member.Type, member.ID)
			}
		}
		d.Sets[id] = members
		return nil
	}); err != nil {
		return nil, err
	}
	return d, nil
}
func (d *AvatarRewardDesign) Expand(rewards []BattleReward) ([]BattleReward, error) {
	var out []BattleReward
	for _, reward := range rewards {
		if reward.Type != 62 {
			out = append(out, reward)
			continue
		}
		if d == nil || reward.Count == 0 || reward.Count > math.MaxInt32 {
			return nil, fmt.Errorf("gamedata: avatar reward design or quantity unavailable")
		}
		members, ok := d.Sets[reward.ID]
		if !ok || len(members) == 0 {
			return nil, fmt.Errorf("gamedata: unknown avatar set %d", reward.ID)
		}
		for _, member := range members {
			if !d.Items[member.Type][member.ID] || member.Count == 0 || member.Count > math.MaxInt32/reward.Count {
				return nil, fmt.Errorf("gamedata: invalid avatar set member")
			}
			member.Count *= reward.Count
			out = append(out, member)
		}
	}
	return out, nil
}
