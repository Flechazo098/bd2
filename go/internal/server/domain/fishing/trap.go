package fishing

import (
	"encoding/json"
	"fmt"
	"math"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type trapCatch struct{ ID, Size uint64 }
type trapState struct {
	Version         int
	Receipt, Cursor int64
	Fish            []trapCatch
}

func loadTrap(ctx command.Context) (trapState, error) {
	t := trapState{Version: 1}
	raw, found, err := ctx.State.LoadEntry("fishing", "state", "trap")
	if err != nil || !found {
		return t, err
	}
	if err = stateio.RequireExactJSONObject(raw, "Version", "Receipt", "Cursor", "Fish"); err != nil {
		return t, err
	}
	if err = json.Unmarshal(raw, &t); err != nil {
		return t, err
	}
	if t.Version != 1 || t.Receipt < 0 || t.Cursor < t.Receipt || len(t.Fish) > math.MaxInt32 {
		return t, fmt.Errorf("fishing: invalid saved trap")
	}
	return t, nil
}

func saveTrap(ctx command.Context, t trapState) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return ctx.State.PutEntry("fishing", "state", "trap", raw)
}

func (s *Service) refreshTrap(n *snapshot, t *trapState) error {
	d, err := s.design.Row("FishingDefaultTable", 25, 0)
	if err != nil {
		return err
	}
	boat, err := s.design.Row("FishingBoatTable", 8, n.Boat)
	if err != nil {
		return err
	}
	frequency, capTime := num(boat, 6), num(d, 19)
	if num(boat, 7) == 0 || frequency == 0 || n.Boat < num(d, 20) {
		return nil
	}
	if frequency > math.MaxInt32 || capTime == 0 || capTime > math.MaxInt32 {
		return fmt.Errorf("fishing: invalid trap timing design")
	}
	now := s.now().Unix()
	if t.Receipt == 0 {
		t.Receipt, t.Cursor = now, now
		return nil
	}
	end := min(now, t.Receipt+int64(capTime))
	if end <= t.Cursor {
		return nil
	}
	count := (end - t.Cursor) / int64(frequency)
	if count == 0 {
		return nil
	}
	gradePool, err := s.design.Row("FishingGradePoolTable", 3, num(d, 18))
	if err != nil {
		return err
	}
	grades, err := ints(gradePool, 2)
	if err != nil {
		return err
	}
	weights, err := ints(gradePool, 4)
	if err != nil {
		return err
	}
	for attempt := int64(1); attempt <= count; attempt++ {
		grade, e := s.weighted(grades, weights)
		if e != nil {
			return e
		}
		var pool []byte
		for _, row := range s.design.Rows("FishingFishPoolTable", 2, num(d, 17)) {
			if num(row, 3) == grade {
				pool = row
				break
			}
		}
		ids, e := ints(pool, 1)
		if e != nil {
			return e
		}
		chances, e := ints(pool, 4)
		if e != nil {
			return e
		}
		id, e := s.weighted(ids, chances)
		if e != nil {
			return e
		}
		registered, known := n.Collection[id]
		if !known || registered.Created > t.Cursor+attempt*int64(frequency) {
			continue
		}
		fish, e := s.design.Row("FishingFishTable", 12, id)
		if e != nil {
			return e
		}
		size, e := s.fishSize(fish, 0)
		if e != nil {
			return e
		}
		t.Fish = append(t.Fish, trapCatch{ID: id, Size: size})
	}
	t.Cursor += count * int64(frequency)
	return nil
}

func (s *Service) fishingExperience(level, exp, gained uint64) (uint64, uint64, error) {
	if exp > math.MaxInt32 || gained > math.MaxInt32-exp {
		return 0, 0, fmt.Errorf("fishing: experience overflow")
	}
	exp += gained
	for {
		row, err := s.design.Row("FishingCharTable", 1, level)
		if err != nil {
			return 0, 0, err
		}
		need := num(row, 2)
		if need == 0 || exp < need {
			return level, exp, nil
		}
		if _, err = s.design.Row("FishingCharTable", 1, level+1); err != nil {
			return level, exp, nil
		}
		exp -= need
		level++
	}
}

func (s *Service) trap(ctx command.Context, path string, req []byte, n *snapshot, key string) ([]byte, error) {
	t, err := loadTrap(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.refreshTrap(n, &t); err != nil {
		return nil, err
	}
	var gained uint64
	var rows [][]byte
	for _, fish := range t.Fish {
		row, e := s.design.Row("FishingFishTable", 12, fish.ID)
		if e != nil {
			return nil, e
		}
		if num(row, 4) > math.MaxInt32-gained {
			return nil, fmt.Errorf("fishing: trap experience overflow")
		}
		gained += num(row, 4)
		rows = append(rows, row)
	}
	level, exp, err := s.fishingExperience(n.Level, n.Exp, gained)
	if err != nil {
		return nil, err
	}
	out := wire.AppendVarint(nil, 1, uint64(t.Receipt))
	if path == "/FishingTrapInfo" {
		for _, fish := range t.Fish {
			item := wire.AppendVarint(nil, 1, fish.ID)
			item = wire.AppendVarint(item, 2, fish.Size)
			out = wire.AppendBytes(out, 2, item)
		}
		out = wire.AppendVarint(out, 3, gained)
		out = wire.AppendVarint(out, 4, level)
		out = wire.AppendVarint(out, 5, exp)
		return out, saveTrap(ctx, t)
	}
	auto := num(req, 2)
	boat, err := s.design.Row("FishingBoatTable", 8, n.Boat)
	if err != nil {
		return nil, err
	}
	if auto > 1 || t.Receipt == 0 || s.now().Unix()-t.Receipt < int64(num(boat, 6)) {
		return nil, fmt.Errorf("fishing: trap settlement unavailable")
	}
	if auto == 0 && (uint64(len(n.Fish)) > n.FishSlots || uint64(len(t.Fish)) > n.FishSlots-uint64(len(n.Fish))) {
		return nil, fmt.Errorf("fishing: fish inventory is full")
	}
	var bundle []byte
	var rewards []gamedata.Reward
	for i, fish := range t.Fish {
		if auto == 1 {
			row := rows[i]
			price, e := s.fishPrice(Item{ID: fish.ID, Size: fish.Size}, num(row, 6))
			if e != nil {
				return nil, e
			}
			if num(row, 8) == 0 || price == 0 {
				return nil, fmt.Errorf("fishing: trap sale price missing")
			}
			rewards = append(rewards, gamedata.Reward{Type: num(row, 8), ID: num(row, 7), Count: price})
		} else {
			if n.Next == 0 || n.Next >= math.MaxInt64 {
				return nil, fmt.Errorf("fishing: fish index exhausted")
			}
			item := Item{Index: n.Next, ID: fish.ID, Size: fish.Size, Count: 1, Time: s.now().Unix()}
			n.Next++
			n.Fish = append(n.Fish, item)
			bundle = wire.AppendBytes(bundle, 11, fishWire(item))
		}
	}
	if len(rewards) > 0 {
		bundle, err = s.economy.Apply(ctx, "fishing:"+key, nil, rewards)
		if err != nil {
			return nil, err
		}
	}
	n.Level, n.Exp = level, exp
	t.Receipt, t.Cursor, t.Fish = s.now().Unix(), s.now().Unix(), nil
	if err = saveTrap(ctx, t); err != nil {
		return nil, err
	}
	out = wire.AppendVarint(nil, 1, uint64(t.Receipt))
	out = wire.AppendBytes(out, 2, bundle)
	out = wire.AppendVarint(out, 3, level)
	out = wire.AppendVarint(out, 4, exp)
	out = wire.AppendVarint(out, 5, gained)
	return out, s.emit(ctx, n, 0, key)
}
