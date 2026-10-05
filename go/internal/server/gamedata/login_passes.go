package gamedata

import (
	"fmt"
	"sort"
)

type LoginPassReward struct {
	ID, TicketID  uint64
	Free, Premium Reward
}
type LoginPassCatalog struct{ Groups map[uint64][]LoginPassReward }

func LoadLoginPassCatalog(root, version string) (*LoginPassCatalog, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &LoginPassCatalog{Groups: map[uint64][]LoginPassReward{}}
	err = readCashMetadata(db, "LoginPassTable", func(raw []byte) error {
		v, e := cashScalars(raw, 5, 6, 1, 4, 3, 2, 9, 8, 7)
		if e != nil {
			return e
		}
		if v[0] == 0 || v[1] == 0 || v[2] == 0 || v[3] == 0 || v[5] == 0 || v[6] == 0 || v[8] == 0 {
			return fmt.Errorf("gamedata: invalid login-pass reward")
		}
		d.Groups[v[0]] = append(d.Groups[v[0]], LoginPassReward{ID: v[1], TicketID: v[2], Free: Reward{Type: v[3], ID: v[4], Count: v[5]}, Premium: Reward{Type: v[6], ID: v[7], Count: v[8]}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for group, rows := range d.Groups {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		for i, r := range rows {
			if r.ID != uint64(i+1) || r.TicketID != rows[0].TicketID {
				return nil, fmt.Errorf("gamedata: invalid login-pass sequence %d", group)
			}
		}
		d.Groups[group] = rows
	}
	return d, nil
}
