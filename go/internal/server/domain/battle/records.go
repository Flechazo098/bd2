package battle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
	"strconv"
)

func (s *Service) records(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	mode := value(request, 2)
	if ctx.State == nil {
		return 0, nil, true, fmt.Errorf("battle: record storage unavailable")
	}
	if path == "/BattleStatisticsInfo" {
		if mode == 0 || mode > 30 {
			return 529, nil, true, fmt.Errorf("battle: invalid statistics mode")
		}
		raw, _, err := ctx.State.LoadEntry("battle", "statistics", strconv.FormatUint(mode, 10))
		if err != nil {
			return 529, nil, true, err
		}
		return 529, wire.AppendBytes(nil, 1, raw), true, nil
	}
	content := value(request, 3)
	if err := gamedata.BattleRecordContent(s.gameDataRoot, s.gameDataVersion, mode, content); err != nil {
		return 0, nil, true, err
	}
	key := fmt.Sprintf("%d:%d", mode, content)
	raw, _, err := ctx.State.LoadEntry("battle", "records", key)
	if err != nil {
		return 0, nil, true, err
	}
	if path == "/BattleRecordInfo" {
		if raw == nil {
			raw = wire.AppendVarint(nil, 1, mode)
			raw = wire.AppendVarint(raw, 2, content)
			raw = wire.AppendBytes(raw, 3, nil)
		}
		return 650, raw, true, nil
	}
	history := one(request, 4)
	damage := value(request, 5)
	if damage > math.MaxInt64 || len(request) > 4<<20 || len(history) == 0 {
		return 651, nil, true, fmt.Errorf("battle: invalid record save")
	}
	if err := validateRecord(history, mode); err != nil {
		return 651, nil, true, err
	}
	out := wire.AppendVarint(nil, 1, mode)
	out = wire.AppendVarint(out, 2, content)
	out = wire.AppendBytes(out, 3, history)
	if best := value(raw, 4); best > damage {
		damage = best
	}
	out = wire.AppendVarint(out, 4, damage)
	return 651, nil, true, ctx.State.PutEntry("battle", "records", key, out)
}

func validateRecord(history []byte, mode uint64) error {
	teams, err := messages(history, 1)
	if err != nil || len(teams) == 0 || len(teams) > 3 {
		return fmt.Errorf("battle: invalid recorded team count")
	}
	seen := map[uint64]bool{}
	for _, team := range teams {
		number := value(team, 1)
		if number > 3 || seen[number] || ((mode == 23 || mode == 26) && number != 0) || ((mode == 8 || mode == 24) && number == 0) {
			return fmt.Errorf("battle: invalid recorded team number")
		}
		seen[number] = true
		chars, err := messages(team, 2)
		if err != nil || len(chars) > 20 {
			return fmt.Errorf("battle: invalid recorded characters")
		}
		ids := map[uint64]bool{}
		for _, char := range chars {
			id := value(char, 1)
			if id == 0 || id > math.MaxInt32 || ids[id] || (value(char, 3) > 100 && value(char, 3) != math.MaxUint64) || value(char, 4) > 100 {
				return fmt.Errorf("battle: invalid recorded character")
			}
			ids[id] = true
			if err := wire.Walk(char, func(wire.Field) error { return nil }); err != nil {
				return err
			}
		}
	}
	turns, err := messages(history, 2)
	if err != nil || len(turns) > 1000 {
		return fmt.Errorf("battle: invalid recorded turns")
	}
	seen = map[uint64]bool{}
	for _, turn := range turns {
		key := value(turn, 1)
		body := one(turn, 2)
		if key == 0 || key > 1000 || seen[key] || value(body, 1) != key {
			return fmt.Errorf("battle: invalid recorded turn")
		}
		seen[key] = true
		if err := wire.Walk(body, func(wire.Field) error { return nil }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) saveStatistics(ctx command.Context, mode uint64, stats []byte) error {
	var damage uint64
	err := wire.Walk(stats, func(f wire.Field) error {
		if f.Number != 1 && f.Number != 2 {
			return nil
		}
		if f.Type != 2 || value(f.Value, 1) == 0 || value(f.Value, 2) > math.MaxInt64 {
			return fmt.Errorf("battle: invalid damage statistics")
		}
		if f.Number == 1 {
			v := value(f.Value, 2)
			if damage > math.MaxInt64-v {
				return fmt.Errorf("battle: damage total overflow")
			}
			damage += v
		}
		return nil
	})
	if err != nil {
		return err
	}
	key := strconv.FormatUint(mode, 10)
	old, _, err := ctx.State.LoadEntry("battle", "statistics", key)
	if err != nil {
		return err
	}
	var previous uint64
	for _, item := range func() [][]byte { entries, _ := messages(old, 1); return entries }() {
		previous += value(item, 2)
	}
	if damage < previous {
		return nil
	}
	return ctx.State.PutEntry("battle", "statistics", key, stats)
}
