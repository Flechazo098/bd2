package gamedata

import (
	"database/sql"
	"fmt"
	"sort"
)

type EventAttendance struct{ ID, Group, Ticket uint64 }
type EventAttendanceGroup struct{ Group, ID, Next uint64 }
type EventAttendanceReward struct {
	Group, ID, Day uint64
	Basic, Premium Reward
}
type EventMissionGroup struct {
	ID, Type, ScheduleType, UsePass, CompleteHide uint64
	Groups                                        []uint64
}
type EventTask struct {
	ID, Group, Type, SubType, Target, PassExp, UnlockPack, UnlockQuest uint64
	Params                                                             []uint64
	Rewards                                                            []Reward
}
type EventPass struct {
	ID, MissionGroup, LevelGroup, Type, ScheduleType, NewbieGroup, NewbieStep uint64
	Core                                                                      Reward
}
type EventPassLevel struct {
	ID, NeedExp, PremiumType uint64
	Basic, Premium           Reward
}
type EventPassBuy struct {
	ID, Type, CashGroup, CashID, CashSales uint64
	// PassBuyTable.UnlockLevel is the number of levels granted by a purchase.
	// LocalText 2403 displays it as "Pass Level +{0}", not a prerequisite.
	LevelsGranted uint64
	Cost          Reward
	Rewards       []Reward
}
type EventTasksDesign struct {
	Attendance        map[uint64]EventAttendance
	AttendanceGroups  map[[2]uint64]EventAttendanceGroup
	AttendanceRewards map[uint64][]EventAttendanceReward
	LimitRewards      map[[2]uint64]uint64
	MissionGroups     map[uint64]EventMissionGroup
	Missions          map[uint64]EventTask
	Passes            map[uint64]EventPass
	PassLevels        map[uint64][]EventPassLevel
	PassBuys          map[uint64][]EventPassBuy
}

func LoadEventTasksDesign(root, version string) (*EventTasksDesign, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	return loadEventTasksDesign(db)
}
func loadEventTasksDesign(db *sql.DB) (*EventTasksDesign, error) {
	d := &EventTasksDesign{Attendance: map[uint64]EventAttendance{}, AttendanceGroups: map[[2]uint64]EventAttendanceGroup{}, AttendanceRewards: map[uint64][]EventAttendanceReward{}, LimitRewards: map[[2]uint64]uint64{}, MissionGroups: map[uint64]EventMissionGroup{}, Missions: map[uint64]EventTask{}, Passes: map[uint64]EventPass{}, PassLevels: map[uint64][]EventPassLevel{}, PassBuys: map[uint64][]EventPassBuy{}}
	each := func(table string, fn func([]byte) error) error {
		rows, e := db.Query("SELECT ProtoBuf FROM " + table)
		if e != nil {
			return e
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p []byte
			if e = rows.Scan(&p); e != nil {
				return e
			}
			if e = fn(p); e != nil {
				return fmt.Errorf("gamedata: %s: %w", table, e)
			}
		}
		return rows.Err()
	}
	scalar := func(p []byte, n int) (uint64, error) { return optionalScalar(p, n) }
	read := func(p []byte, m map[int]*uint64) error {
		for n, target := range m {
			v, e := scalar(p, n)
			if e != nil {
				return e
			}
			*target = v
		}
		return nil
	}
	reward := func(p []byte, t, i, c int) (Reward, error) {
		r := Reward{}
		e := read(p, map[int]*uint64{t: &r.Type, i: &r.ID, c: &r.Count})
		return r, e
	}
	if e := each("AttendanceAlwaysTable", func(p []byte) error {
		r := EventAttendance{}
		e := read(p, map[int]*uint64{3: &r.ID, 1: &r.Group, 2: &r.Ticket})
		d.Attendance[r.ID] = r
		return e
	}); e != nil {
		return nil, e
	}
	if e := each("AttendanceAlwaysGroupTable", func(p []byte) error {
		r := EventAttendanceGroup{}
		e := read(p, map[int]*uint64{1: &r.Group, 2: &r.ID, 3: &r.Next})
		d.AttendanceGroups[[2]uint64{r.Group, r.ID}] = r
		return e
	}); e != nil {
		return nil, e
	}
	if e := each("AttendanceAlwaysRewardTable", func(p []byte) error {
		r := EventAttendanceReward{}
		if e := read(p, map[int]*uint64{2: &r.Group, 3: &r.ID, 1: &r.Day}); e != nil {
			return e
		}
		var e error
		r.Basic, e = reward(p, 9, 8, 7)
		if e != nil {
			return e
		}
		r.Premium, e = reward(p, 6, 5, 4)
		d.AttendanceRewards[r.Group] = append(d.AttendanceRewards[r.Group], r)
		return e
	}); e != nil {
		return nil, e
	}
	if e := each("AttendanceLimitRewardTable", func(p []byte) error {
		var g, i, b uint64
		e := read(p, map[int]*uint64{1: &g, 2: &i, 3: &b})
		d.LimitRewards[[2]uint64{g, i}] = b
		return e
	}); e != nil {
		return nil, e
	}
	if e := each("EventMissionGroupTable", func(p []byte) error {
		r := EventMissionGroup{}
		if e := read(p, map[int]*uint64{5: &r.ID, 1: &r.Type, 8: &r.ScheduleType, 10: &r.UsePass, 6: &r.CompleteHide}); e != nil {
			return e
		}
		var e error
		r.Groups, e = packedInts(p, 7)
		d.MissionGroups[r.ID] = r
		return e
	}); e != nil {
		return nil, e
	}
	if e := each("MissionTable", func(p []byte) error {
		g, e := scalar(p, 7)
		if e != nil {
			return e
		}
		if g != 2 {
			return nil
		}
		r := EventTask{}
		if e = read(p, map[int]*uint64{8: &r.ID, 6: &r.Group, 3: &r.Type, 1: &r.SubType, 4: &r.Target, 12: &r.PassExp, 19: &r.UnlockPack, 20: &r.UnlockQuest}); e != nil {
			return e
		}
		r.Params, e = packedInts(p, 2)
		if e != nil {
			return e
		}
		v, e := reward(p, 15, 14, 13)
		if e != nil {
			return e
		}
		if v.Type > 0 && v.Count > 0 {
			r.Rewards = []Reward{v}
		}
		d.Missions[r.ID] = r
		return nil
	}); e != nil {
		return nil, e
	}
	if e := each("PassTable", func(p []byte) error {
		r := EventPass{}
		if e := read(p, map[int]*uint64{6: &r.ID, 5: &r.MissionGroup, 11: &r.LevelGroup, 13: &r.Type, 15: &r.ScheduleType, 9: &r.NewbieGroup, 10: &r.NewbieStep}); e != nil {
			return e
		}
		var e error
		r.Core, e = reward(p, 4, 3, 2)
		d.Passes[r.ID] = r
		return e
	}); e != nil {
		return nil, e
	}
	if e := each("PassLevelTable", func(p []byte) error {
		r := EventPassLevel{}
		var g uint64
		if e := read(p, map[int]*uint64{4: &g, 5: &r.ID, 6: &r.NeedExp, 10: &r.PremiumType}); e != nil {
			return e
		}
		var e error
		r.Basic, e = reward(p, 3, 2, 1)
		if e != nil {
			return e
		}
		r.Premium, e = reward(p, 9, 8, 7)
		d.PassLevels[g] = append(d.PassLevels[g], r)
		return e
	}); e != nil {
		return nil, e
	}
	if e := each("PassBuyTable", func(p []byte) error {
		r := EventPassBuy{}
		var g uint64
		if e := read(p, map[int]*uint64{7: &g, 8: &r.ID, 12: &r.Type, 13: &r.LevelsGranted, 4: &r.CashGroup, 5: &r.CashID, 6: &r.CashSales}); e != nil {
			return e
		}
		var e error
		r.Cost, e = reward(p, 3, 2, 1)
		if e != nil {
			return e
		}
		rs, e := monsterHuntRewardArrays(p, 11, 10, 9)
		if e != nil {
			return e
		}
		for _, v := range rs {
			r.Rewards = append(r.Rewards, Reward{v.Type, v.ID, v.Count}) //nolint:staticcheck // S1016
		}
		d.PassBuys[g] = append(d.PassBuys[g], r)
		return nil
	}); e != nil {
		return nil, e
	}
	for g, rs := range d.AttendanceRewards {
		sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
		d.AttendanceRewards[g] = rs
	}
	for g, rs := range d.PassLevels {
		sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
		d.PassLevels[g] = rs
	}
	return d, nil
}
