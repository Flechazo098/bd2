package gamedata

import (
	"bd2server/internal/server/wire"
	"fmt"
	"sort"
)

// EventActionRow preserves current-version design fields without introducing
// hard-coded event IDs. Runtime modules consume the named table and fields.
type EventActionRow struct {
	Values       map[int]uint64
	Rewards      []Reward
	EventRewards []Reward
	Text         map[int]string
}

func (r EventActionRow) V(field int) uint64 { return r.Values[field] }

type EventActionsDesign struct {
	Tables       map[string][]EventActionRow
	SpawnRewards map[[2]uint64]Reward
}

func (d *EventActionsDesign) Row(table string, idField int, id uint64) (EventActionRow, bool) {
	for _, r := range d.Tables[table] {
		if r.V(idField) == id {
			return r, true
		}
	}
	return EventActionRow{}, false
}
func LoadEventActionsDesign(root, version string) (*EventActionsDesign, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	d := &EventActionsDesign{Tables: map[string][]EventActionRow{}, SpawnRewards: map[[2]uint64]Reward{}}
	for _, table := range []string{"FieldEventDefaultTable", "FieldSpawnEventTable", "FieldEventMonsterTable", "FireworksTable", "VotingEventTable", "VotingCandidateTable", "VotingRoundTable", "VotingCountRewardTable", "VotingSeasonTable", "FriendshipSpecialEpisodeTable", "NpcQuizTable", "TacticsBingoGroupTable", "TacticsBingoTable", "CafeteriaDefaultTable", "CafeteriaEventTable"} {
		rows, e := db.Query("SELECT ProtoBuf FROM " + table)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var p []byte
			if e = rows.Scan(&p); e != nil {
				rows.Close()
				return nil, e
			}
			r := EventActionRow{Values: map[int]uint64{}, Text: map[int]string{}}
			if e = wire.Walk(p, func(f wire.Field) error {
				if f.Type == 0 {
					v, _, er := wire.Varint(p, f.Number)
					if er != nil {
						return er
					}
					r.Values[f.Number] = v
				} else if f.Type == 2 {
					r.Text[f.Number] = string(f.Value)
				}
				return nil
			}); e != nil {
				rows.Close()
				return nil, e
			}
			var array [3]int
			switch table {
			case "FriendshipSpecialEpisodeTable":
				array = [3]int{9, 8, 7}
			case "NpcQuizTable":
				array = [3]int{12, 11, 10}
			}
			if array[0] > 0 {
				rs, e := monsterHuntRewardArrays(p, array[0], array[1], array[2])
				if e != nil {
					rows.Close()
					return nil, e
				}
				for _, x := range rs {
					r.Rewards = append(r.Rewards, Reward{x.Type, x.ID, x.Count})
				}
			}
			if table == "FriendshipSpecialEpisodeTable" {
				rs, e := monsterHuntRewardArrays(p, 4, 3, 2)
				if e != nil {
					rows.Close()
					return nil, e
				}
				for _, x := range rs {
					r.EventRewards = append(r.EventRewards, Reward{x.Type, x.ID, x.Count})
				}
			}
			if table == "FireworksTable" {
				r.Rewards = []Reward{{r.V(9), r.V(8), r.V(7)}}
			}
			if table == "VotingCountRewardTable" {
				r.Rewards = []Reward{{r.V(5), r.V(4), r.V(3)}}
			}
			d.Tables[table] = append(d.Tables[table], r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	packs := map[uint64]bool{}
	for _, r := range d.Tables["FieldEventMonsterTable"] {
		packs[r.V(5)] = true
	}
	for pack := range packs {
		pdb, closePack, e := openPackDatabase(root, version, int(pack))
		if e != nil {
			return nil, e
		}
		for _, r := range d.Tables["FieldEventMonsterTable"] {
			if r.V(5) != pack {
				continue
			}
			var p []byte
			if e = pdb.QueryRow("SELECT ProtoBuf FROM FieldMonsterTable WHERE id=?", r.V(1)).Scan(&p); e != nil {
				closePack()
				return nil, fmt.Errorf("event spawn monster %d: %w", r.V(1), e)
			}
			t, e := optionalScalar(p, 29)
			if e != nil {
				closePack()
				return nil, e
			}
			id, e := optionalScalar(p, 28)
			if e != nil {
				closePack()
				return nil, e
			}
			count, e := optionalScalar(p, 27)
			if e != nil {
				closePack()
				return nil, e
			}
			d.SpawnRewards[[2]uint64{pack, r.V(1)}] = Reward{t, id, count}
		}
		closePack()
	}
	for table, rows := range d.Tables {
		sort.Slice(rows, func(i, j int) bool { return rows[i].V(2) < rows[j].V(2) })
		d.Tables[table] = rows
	}
	return d, nil
}
