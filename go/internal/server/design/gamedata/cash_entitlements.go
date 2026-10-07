package gamedata

import (
	"database/sql"
	"fmt"
	"sort"
)

type CashAttendanceReward struct {
	ID     uint64
	Reward BattleReward
}
type CashEntitlementDesign struct {
	AvatarSets      map[uint64]bool
	TicketTypes     map[uint64]uint64
	Attendance      map[uint64][]CashAttendanceReward
	AttendanceTypes map[uint64]uint64
}

func LoadCashEntitlementDesign(root, version string) (*CashEntitlementDesign, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	return loadCashEntitlementDesign(db)
}
func loadCashEntitlementDesign(db *sql.DB) (*CashEntitlementDesign, error) {
	d := &CashEntitlementDesign{AvatarSets: map[uint64]bool{}, TicketTypes: map[uint64]uint64{}, Attendance: map[uint64][]CashAttendanceReward{}, AttendanceTypes: map[uint64]uint64{}}
	if e := readCashMetadata(db, "AvatarSetTable", func(raw []byte) error {
		v, e := cashScalars(raw, 6)
		if e != nil {
			return e
		}
		if v[0] == 0 {
			return fmt.Errorf("gamedata: invalid avatar set")
		}
		d.AvatarSets[v[0]] = true
		return nil
	}); e != nil {
		return nil, e
	}
	if e := readCashMetadata(db, "ContentTicketTable", func(raw []byte) error {
		v, e := cashScalars(raw, 4, 8)
		if e != nil {
			return e
		}
		d.TicketTypes[v[0]] = v[1]
		return nil
	}); e != nil {
		return nil, e
	}
	if e := readCashMetadata(db, "ContentOpenTable", func(raw []byte) error {
		v, e := cashScalars(raw, 6, 2)
		if e != nil {
			return e
		}
		if d.TicketTypes[v[0]] == 2 {
			d.AttendanceTypes[v[0]] = v[1]
		}
		return nil
	}); e != nil {
		return nil, e
	}
	if e := readCashMetadata(db, "AttendanceRewardTable", func(raw []byte) error {
		v, e := cashScalars(raw, 1, 2, 5, 4, 3)
		if e != nil {
			return e
		}
		if v[0] == 0 || v[1] == 0 || v[2] == 0 || v[4] == 0 {
			return fmt.Errorf("gamedata: invalid cash attendance reward")
		}
		d.Attendance[v[0]] = append(d.Attendance[v[0]], CashAttendanceReward{v[1], BattleReward{Type: v[2], ID: v[3], Count: v[4]}})
		return nil
	}); e != nil {
		return nil, e
	}
	for ticket, rows := range d.Attendance {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		for i, r := range rows {
			if r.ID != uint64(i+1) {
				return nil, fmt.Errorf("gamedata: non-contiguous cash attendance %d", ticket)
			}
		}
		if d.TicketTypes[ticket] != 2 && d.TicketTypes[ticket] != 3 {
			return nil, fmt.Errorf("gamedata: missing cash attendance ticket %d", ticket)
		}
		d.Attendance[ticket] = rows
	}
	return d, nil
}
