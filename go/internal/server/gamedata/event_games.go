package gamedata

import "fmt"

type EventGameReward struct {
	ID, Count, LineType, LineIndex, Slot, Weight, Weight2 uint64
	Members                                               []uint64
	Rewards                                               []BattleReward
}
type EventGame struct {
	ID, Type, Columns, CostType, CostID, Cost, ScaffoldGroup, Controller, Free, Pity, MaxConsume uint64
	Cells, Lines, Complete                                                                       []EventGameReward
	Moves                                                                                        []struct{ ID, Min, Max uint64 }
	RewardGroup                                                                                  uint64
}

func LoadEventGame(root, version string, kind, id uint64) (*EventGame, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	tables := map[uint64]string{12: "MiniGameBoardTable", 13: "MiniGameBingoTable", 17: "MiniGamePuzzleEventTable", 19: "MiniGameRouletteTable"}
	table := tables[kind]
	if table == "" {
		return nil, fmt.Errorf("gamedata: unsupported event game")
	}
	var raw []byte
	if e = db.QueryRow("SELECT ProtoBuf FROM "+table+" WHERE id=?", id).Scan(&raw); e != nil {
		return nil, e
	}
	d := &EventGame{ID: id, Type: kind}
	var cellGroup, lineGroup, completeGroup uint64
	fields := map[int]*uint64{}
	switch kind {
	case 12:
		fields = map[int]*uint64{4: &d.Cost, 5: &d.CostID, 6: &d.CostType, 7: &completeGroup, 9: &d.Controller, 11: &d.ScaffoldGroup}
	case 13:
		fields = map[int]*uint64{1: &completeGroup, 2: &lineGroup, 3: &cellGroup, 5: &d.Columns, 7: &d.Cost, 8: &d.CostID, 9: &d.CostType}
	case 17:
		fields = map[int]*uint64{1: &d.Columns, 3: &d.Cost, 4: &d.CostID, 5: &d.CostType, 6: &completeGroup, 7: &cellGroup}
	case 19:
		fields = map[int]*uint64{1: &d.Free, 3: &d.Cost, 4: &d.CostID, 5: &d.MaxConsume, 6: &d.CostType, 7: &d.Pity, 8: &completeGroup, 9: &cellGroup}
	}
	for n, p := range fields {
		*p, e = optionalScalar(raw, n)
		if e != nil {
			return nil, e
		}
	}
	d.RewardGroup = cellGroup
	load := func(table string, group uint64, mode string) ([]EventGameReward, error) {
		rows, e := db.Query("SELECT ProtoBuf FROM "+table+" WHERE groupId=? ORDER BY id", group)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		var out []EventGameReward
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				return nil, e
			}
			r := EventGameReward{}
			idField, countField, typ, ids, counts := 3, 1, 6, 5, 4
			switch mode {
			case "scaffold":
				idField = 2
				countField = 0
				typ = 5
				ids = 4
				counts = 3
			case "line":
				typ = 8
				ids = 7
				counts = 6
				r.LineType, e = optionalScalar(b, 5)
				if e != nil {
					return nil, e
				}
				r.LineIndex, e = optionalScalar(b, 4)
			case "roulette":
				idField = 2
				countField = 0
				typ = 7
				ids = 6
				counts = 5
				r.Slot, e = optionalScalar(b, 8)
				if e != nil {
					return nil, e
				}
				r.Weight, e = optionalScalar(b, 3)
				if e != nil {
					return nil, e
				}
				r.Weight2, e = optionalScalar(b, 4)
			case "puzzlecomplete":
				typ = 7
				ids = 6
				counts = 5
				r.Members, e = packedInts(b, 4)
			case "puzzle":
				r.Slot, e = optionalScalar(b, 7)
			}
			if e != nil {
				return nil, e
			}
			r.ID, e = optionalScalar(b, idField)
			if e != nil {
				return nil, e
			}
			if countField > 0 {
				r.Count, e = optionalScalar(b, countField)
				if e != nil {
					return nil, e
				}
			}
			r.Rewards, e = eventGameRewards(b, typ, ids, counts)
			if e != nil {
				return nil, e
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	switch kind {
	case 12:
		d.Cells, e = load("MiniGameScaffoldTable", d.ScaffoldGroup, "scaffold")
		if e == nil {
			d.Complete, e = load("MiniGameCompleteRewardTable", completeGroup, "default")
		}
		if e != nil {
			return nil, e
		}
		rows, e := db.Query("SELECT ProtoBuf FROM MiniGameMoveControllerTable WHERE groupId=? ORDER BY id", d.Controller)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				rows.Close()
				return nil, e
			}
			i, e := optionalScalar(b, 3)
			if e != nil {
				rows.Close()
				return nil, e
			}
			min, _ := optionalScalar(b, 5)
			max, _ := optionalScalar(b, 4)
			d.Moves = append(d.Moves, struct{ ID, Min, Max uint64 }{i, min, max})
		}
		e = rows.Err()
		rows.Close()
	case 13:
		d.Cells, e = load("BingoRewardGroupTable", cellGroup, "default")
		if e == nil {
			d.Lines, e = load("BingoLineRewardGroupTable", lineGroup, "line")
		}
		if e == nil {
			d.Complete, e = load("BingoCompleteRewardGroupTable", completeGroup, "default")
		}
	case 17:
		d.Cells, e = load("PuzzleRewardGroupTable", cellGroup, "puzzle")
		if e == nil {
			d.Complete, e = load("PuzzleCompleteRewardGroupTable", completeGroup, "puzzlecomplete")
		}
	case 19:
		d.Cells, e = load("RouletteRewardGroupTable", cellGroup, "roulette")
		if e == nil {
			d.Complete, e = load("RouletteAccumulatedRewardTable", completeGroup, "default")
		}
	}
	if e != nil {
		return nil, e
	}
	if d.Cost == 0 || len(d.Cells) == 0 {
		return nil, fmt.Errorf("gamedata: empty event game rules")
	}
	return d, nil
}

func eventGameRewards(raw []byte, typ, ids, counts int) ([]BattleReward, error) {
	ts, e := packedInts(raw, typ)
	if e != nil {
		return nil, e
	}
	is, e := packedInts(raw, ids)
	if e != nil {
		return nil, e
	}
	cs, e := packedInts(raw, counts)
	if e != nil {
		return nil, e
	}
	if len(ts) == 0 && len(cs) == 0 {
		return nil, nil
	}
	if len(is) == 0 {
		is = make([]uint64, len(ts))
	}
	if len(ts) != len(is) || len(ts) != len(cs) {
		return nil, fmt.Errorf("gamedata: event game reward arrays differ")
	}
	var out []BattleReward
	for i, t := range ts {
		if t != 0 && cs[i] != 0 {
			out = append(out, BattleReward{Type: t, ID: is[i], Count: cs[i]})
		}
	}
	return out, nil
}
