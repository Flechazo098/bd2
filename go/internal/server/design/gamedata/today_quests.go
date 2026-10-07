package gamedata

import (
	"fmt"
)

// TodayQuest is a node of the weekly NPC commission chain. The historical
// table/packet name says Today, but current LocalText397 says quests per week.
type TodayQuest struct {
	ID, PackID, PriorID, NextID, ConditionType, ConditionCount, ReputationCompleteID int
	MagicValues, GiveItemIDs                                                         []uint64
	Rewards                                                                          []Reward
}
type TodayQuestCatalog struct {
	Quests                             map[int]TodayQuest
	Limit, PostCount, AchievementScore int
	Reset                              FieldResetSchedule
}

func LoadTodayQuests(root, version string) (*TodayQuestCatalog, error) {
	reset, err := LoadFieldResetSchedule(root, version)
	if err != nil {
		return nil, err
	}
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	c := &TodayQuestCatalog{Quests: map[int]TodayQuest{}, Reset: reset}
	var defaults []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&defaults); err != nil {
		return nil, err
	}
	for field, dst := range map[int]*int{110: &c.AchievementScore, 111: &c.Limit, 112: &c.PostCount} {
		v, e := optionalScalar(defaults, field)
		if e != nil || v == 0 || v > 2147483647 {
			return nil, fmt.Errorf("gamedata: invalid today defaults %d", field)
		}
		*dst = int(v)
	}
	rows, err := db.Query("SELECT id,ProtoBuf FROM TodayQuestTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		q := TodayQuest{ID: id}
		for field, dst := range map[int]*int{22: &q.PackID, 23: &q.PriorID, 21: &q.NextID, 10: &q.ConditionType, 9: &q.ConditionCount, 27: &q.ReputationCompleteID} {
			v, e := optionalScalar(raw, field)
			if e != nil || v > 2147483647 {
				return nil, fmt.Errorf("gamedata: invalid today quest %d field %d", id, field)
			}
			*dst = int(v)
		}
		typ, e := optionalScalar(raw, 31)
		if e != nil || typ != 2 || id <= 0 || q.PackID <= 0 || q.ConditionCount <= 0 {
			return nil, fmt.Errorf("gamedata: invalid today quest %d", id)
		}
		switch q.ConditionType {
		case 1, 2, 9, 18, 19:
		default:
			return nil, fmt.Errorf("gamedata: unsupported today condition %d", q.ConditionType)
		}
		if q.MagicValues, err = packedInts(raw, 19); err != nil {
			return nil, err
		}
		if q.GiveItemIDs, err = packedInts(raw, 17); err != nil {
			return nil, err
		}
		ts, e := packedInts(raw, 30)
		if e != nil {
			return nil, e
		}
		ids, e := packedInts(raw, 29)
		if e != nil {
			return nil, e
		}
		ns, e := packedInts(raw, 28)
		if e != nil {
			return nil, e
		}
		if len(ts) != len(ids) || len(ids) != len(ns) {
			return nil, fmt.Errorf("gamedata: today quest %d reward arrays", id)
		}
		for i, t := range ts {
			if t == 0 {
				return nil, fmt.Errorf("gamedata: today quest zero reward")
			}
			q.Rewards = append(q.Rewards, Reward{Type: t, ID: ids[i], Count: ns[i]})
		}
		c.Quests[id] = q
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, q := range c.Quests {
		seen := map[int]bool{}
		for n := q.ID; n != 0; n = c.Quests[n].NextID {
			if seen[n] {
				return nil, fmt.Errorf("gamedata: cyclic today chain")
			}
			seen[n] = true
			node, ok := c.Quests[n]
			if !ok || node.PackID != q.PackID {
				return nil, fmt.Errorf("gamedata: broken today chain")
			}
			if node.NextID != 0 && c.Quests[node.NextID].PriorID != n {
				return nil, fmt.Errorf("gamedata: asymmetric today chain")
			}
		}
	}
	return c, nil
}
