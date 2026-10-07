package gamedata

import (
	"fmt"
	"sort"
)

type CashBonusReward struct {
	ID, RequireCount uint64
	Reward           Reward
}

type CashBonusCatalog struct{ Groups map[uint64][]CashBonusReward }

func LoadCashBonusCatalog(root, version string) (*CashBonusCatalog, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &CashBonusCatalog{Groups: map[uint64][]CashBonusReward{}}
	err = readCashMetadata(db, "CashBonusTable", func(raw []byte) error {
		v, err := cashScalars(raw, 1, 2, 3, 6, 5, 4)
		if err != nil {
			return err
		}
		if v[0] == 0 || v[1] == 0 || v[2] == 0 || v[3] == 0 || v[5] == 0 {
			return fmt.Errorf("gamedata: invalid cash bonus")
		}
		d.Groups[v[0]] = append(d.Groups[v[0]], CashBonusReward{v[1], v[2], Reward{Type: v[3], ID: v[4], Count: v[5]}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for group, rows := range d.Groups {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		for i, row := range rows {
			if i > 0 && row.ID == rows[i-1].ID {
				return nil, fmt.Errorf("gamedata: duplicate cash bonus group=%d id=%d", group, row.ID)
			}
		}
		d.Groups[group] = rows
	}
	return d, nil
}
