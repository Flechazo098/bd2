package gamedata

import (
	"fmt"
	"math"
)

type ResourceItemDesign struct {
	Selections map[uint64]map[uint64]BattleReward
}

func LoadResourceItemDesign(root, version string) (*ResourceItemDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	groups := map[uint64]map[uint64]BattleReward{}
	if err := readCashMetadata(db, "RewardGroupTable", func(raw []byte) error {
		id, err := optionalScalar(raw, 3)
		if err != nil {
			return err
		}
		rewards, err := eventGameRewards(raw, 6, 5, 4)
		if err != nil {
			return err
		}
		options := map[uint64]BattleReward{}
		for _, r := range rewards {
			if r.Type == 11 && r.ID != 0 && r.Count > 0 {
				options[r.ID] = r
			}
		}
		groups[id] = options
		return nil
	}); err != nil {
		return nil, err
	}
	selections := map[uint64]map[uint64]BattleReward{}
	if err := readCashMetadata(db, "CostumeSelectTable", func(raw []byte) error {
		id, err := optionalScalar(raw, 2)
		if err != nil {
			return err
		}
		group, err := optionalScalar(raw, 1)
		if err != nil {
			return err
		}
		selections[id] = groups[group]
		return nil
	}); err != nil {
		return nil, err
	}
	d := &ResourceItemDesign{Selections: map[uint64]map[uint64]BattleReward{}}
	if err := readCashMetadata(db, "ResourceTable", func(raw []byte) error {
		kind, err := optionalScalar(raw, 13)
		if err != nil || kind != 6 {
			return err
		}
		id, err := optionalScalar(raw, 4)
		if err != nil {
			return err
		}
		selection, err := optionalScalar(raw, 9)
		if err != nil {
			return err
		}
		if id == 0 || len(selections[selection]) == 0 {
			return fmt.Errorf("gamedata: resource %d has no costume choices", id)
		}
		d.Selections[id] = selections[selection]
		return nil
	}); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *ResourceItemDesign) Select(resource, selection, count uint64) ([]BattleReward, error) {
	if d == nil || count == 0 || count > math.MaxInt32 {
		return nil, fmt.Errorf("gamedata: invalid resource item use")
	}
	r, ok := d.Selections[resource][selection]
	if !ok || r.Type != 11 || r.ID != selection || r.Count == 0 || r.Count > math.MaxInt32/count {
		return nil, fmt.Errorf("gamedata: unavailable costume choice")
	}
	r.Count *= count
	return []BattleReward{r}, nil
}
