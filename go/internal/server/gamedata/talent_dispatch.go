package gamedata

import (
	"bd2server/internal/server/wire"
	"fmt"
	"time"
)

type TalentDispatchDesign struct {
	ID, Level, Seconds, RewardGroup uint64
	Prefab                          string
	Reset                           FieldResetSchedule
	reward                          *DispatchDesign
}

func (d TalentDispatchDesign) Roll(draw func(uint64) (uint64, error)) ([]BattleReward, error) {
	return d.reward.Roll(1, draw)
}

// Current skill descriptions 27151-27155/27176-27180 say dispatch returns at
// 09:00. DispatchTime is still used by the client's progress slider; completion
// follows GameDefault's reset clock rather than start + that legacy duration.
func (d TalentDispatchDesign) EndTime(now time.Time) time.Time {
	offset := 9*time.Hour - d.Reset.DailyReset
	shifted := now.UTC().Add(offset)
	return time.Date(shifted.Year(), shifted.Month(), shifted.Day()+1, 0, 0, 0, 0, time.UTC).Add(-offset)
}
func LoadTalentDispatchDesign(root, version string) (map[uint64]TalentDispatchDesign, error) {
	reset, e := LoadFieldResetSchedule(root, version)
	if e != nil {
		return nil, e
	}
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	rows, e := db.Query("SELECT id,ProtoBuf FROM DispatchTable")
	if e != nil {
		return nil, e
	}
	type row struct {
		id  uint64
		raw []byte
	}
	var rs []row
	for rows.Next() {
		var r row
		if e = rows.Scan(&r.id, &r.raw); e != nil {
			_ = rows.Close()
			return nil, e
		}
		rs = append(rs, r)
	}
	if e = rows.Err(); e != nil {
		_ = rows.Close()
		return nil, e
	}
	_ = rows.Close()
	out := map[uint64]TalentDispatchDesign{}
	for _, r := range rs {
		d := TalentDispatchDesign{ID: r.id, Reset: reset}
		d.Level, e = optionalScalar(r.raw, 2)
		if e != nil {
			return nil, e
		}
		d.Seconds, e = optionalScalar(r.raw, 4)
		if e != nil {
			return nil, e
		}
		d.RewardGroup, e = optionalScalar(r.raw, 8)
		if e != nil {
			return nil, e
		}
		p, _, e := wire.Bytes(r.raw, 7)
		if e != nil {
			return nil, e
		}
		d.Prefab = string(p)
		if d.ID == 0 || d.Seconds == 0 || d.Prefab == "" {
			return nil, fmt.Errorf("gamedata: invalid talent dispatch")
		}
		g, e := loadDispatchRewardGroup(db, d.RewardGroup, map[uint64]bool{})
		if e != nil {
			return nil, e
		}
		d.reward = &DispatchDesign{Rewards: []BattleReward{{Type: 9, ID: d.RewardGroup, Count: 1}}, boxes: map[uint64]*dispatchGroup{d.RewardGroup: g}}
		out[d.ID] = d
	}
	return out, nil
}
