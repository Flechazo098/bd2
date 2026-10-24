package eventplay

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
)

func (s *Service) packManRule(mode uint64) ([]byte, error) {
	if len(s.design.Tables["PackManDefaultTable"]) == 0 {
		return nil, fmt.Errorf("eventplay: PackMan default missing")
	}
	group := num(s.design.Tables["PackManDefaultTable"][0], 15)
	for _, row := range s.design.Rows("PackManModeTable", 2, group) {
		if num(row, 5) == mode {
			return s.design.Row("PackManModeRuleTable", 2, num(row, 4))
		}
	}
	return nil, fmt.Errorf("eventplay: PackMan mode missing")
}

func (s *Service) validatePackManRecord(a Run, progress []byte, stageClear bool) (uint64, error) {
	if len(progress) == 0 {
		return 0, fmt.Errorf("eventplay: missing PackMan history")
	}
	for field := 1; field <= 6; field++ {
		value := num(progress, field)
		if value > math.MaxInt32 || value < num(a.PackRecord, field) {
			return 0, fmt.Errorf("eventplay: PackMan cumulative history regressed")
		}
	}
	elapsed := num(progress, 1)
	if now := uint64(s.now().UnixMilli()); now < a.Started || elapsed > (now-a.Started)/1000+1 {
		return 0, fmt.Errorf("eventplay: PackMan play time exceeds run duration")
	}
	expectedEscapes := a.Stage - 1
	if stageClear {
		expectedEscapes++
	}
	if num(progress, 6) != expectedEscapes {
		return 0, fmt.Errorf("eventplay: PackMan escape count differs from committed stages")
	}
	rule, err := s.packManRule(a.Mode)
	if err != nil {
		return 0, err
	}
	if limit := num(rule, 8); limit > 0 && elapsed*1000 > limit+num(rule, 6)*expectedEscapes+1000 {
		return 0, fmt.Errorf("eventplay: PackMan time limit exceeded")
	}
	coinCounts := map[uint64]uint64{}
	seen := map[string]bool{}
	lastCoinTime := uint64(0)
	err = wire.Walk(progress, func(f wire.Field) error {
		switch f.Number {
		case 7:
			if f.Type != 2 {
				return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, f.Number, f.Type)
			}
			typ, x, y, t := num(f.Value, 1), num(f.Value, 2), num(f.Value, 3), num(f.Value, 4)
			if typ < 1 || typ > 3 || t > elapsed*1000+1000 || t < lastCoinTime {
				return fmt.Errorf("eventplay: invalid PackMan coin record")
			}
			identity := fmt.Sprintf("%d:%d", x, y)
			if seen[identity] {
				return fmt.Errorf("eventplay: PackMan coin collected twice")
			}
			seen[identity] = true
			coinCounts[typ]++
			lastCoinTime = t
		case 8:
			if f.Type != 2 {
				return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, f.Number, f.Type)
			}
			row, e := s.design.Row("PackManItemTable", 4, num(f.Value, 2))
			if e != nil {
				return e
			}
			if num(row, 7) != num(f.Value, 1) || num(f.Value, 3) > elapsed*1000+1000 {
				return fmt.Errorf("eventplay: invalid PackMan item use")
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for typ := uint64(1); typ <= 3; typ++ {
		if num(progress, int(typ)+1)-num(a.PackRecord, int(typ)+1) != coinCounts[typ] {
			return 0, fmt.Errorf("eventplay: PackMan coin totals differ from stage history")
		}
	}
	if num(progress, 5) > num(progress, 2)+num(progress, 3)+num(progress, 4) {
		return 0, fmt.Errorf("eventplay: PackMan chain exceeds coins")
	}
	if a.Mode == 1 {
		if num(progress, 5) != 0 {
			return 0, fmt.Errorf("eventplay: normal PackMan has no chain score")
		}
		return 0, nil
	}
	var score uint64
	for field := 1; field <= 6; field++ {
		row, e := s.design.Row("PackManScoreTable", 1, uint64(field))
		if e != nil {
			return 0, e
		}
		value, unit := num(progress, field), num(row, 2)
		if unit > 0 && value > (math.MaxInt32-score)/unit {
			return 0, fmt.Errorf("eventplay: PackMan score overflow")
		}
		score += value * unit
	}
	return score, nil
}

func (s *Service) endPackMan(ctx command.Context, req []byte, a Run, st *snapshot, key string) ([]byte, error) {
	endType := num(req, 2)
	if endType < 1 || endType > 3 {
		return nil, fmt.Errorf("eventplay: invalid PackMan end type")
	}
	if id := num(req, 3); id > 0 {
		if _, err := s.design.Row("PackManChaserTable", 2, id); err != nil {
			return nil, err
		}
	}
	rule, err := s.packManRule(a.Mode)
	if err != nil {
		return nil, err
	}
	if endType == 3 && (num(rule, 4) == 0 || a.Stage != num(rule, 4)) {
		return nil, fmt.Errorf("eventplay: PackMan final stage not reached")
	}
	if endType == 2 && num(rule, 8) == 0 {
		return nil, fmt.Errorf("eventplay: PackMan mode has no timer")
	}
	progress, _, err := wire.Bytes(req, 4)
	if err != nil {
		return nil, err
	}
	score, err := s.validatePackManRecord(a, progress, false)
	if err != nil {
		return nil, err
	}
	if score != num(req, 5) {
		return nil, fmt.Errorf("eventplay: PackMan score differs from configured formula")
	}
	if endType == 2 && num(progress, 1)*1000+1000 < num(rule, 8)+num(rule, 6)*(a.Stage-1) {
		return nil, fmt.Errorf("eventplay: PackMan timer has not expired")
	}
	r := playRecord(st, a.UID, a.Game, 0, a.Mode, a.Family)
	newRecord := a.Mode == 2 && score > r.Best
	if newRecord {
		r.Best = score
		r.AchievedAt = uint64(s.now().UnixMilli())
	}
	if endType == 3 {
		r.Clears["clear"] = true
	}
	st.Records[recKey(a.UID, 0, a.Mode, a.Family)] = r
	if s.onProgress != nil {
		events := []conditions.Event{{Type: conditions.PackManPlayCount, Count: 1}}
		// Request field 3 is the chaser that caught the player; zero otherwise.
		if chaser := num(req, 3); chaser > 0 {
			caught := conditions.PackManNormalGameOver
			if a.Mode == 2 {
				caught = conditions.PackManChallengeGameOver
			}
			events = append(events, conditions.Event{Type: caught, Sub: chaser, Count: 1})
		}
		if endType == 3 {
			events = append(events, conditions.Event{Type: conditions.PackManNormalClear, Count: 1})
		}
		if a.Mode == 2 && endType == 2 {
			events = append(events, conditions.Event{Type: conditions.PackManChallengeTimeoutScore, Sub: score, Count: 1})
		}
		for _, event := range events {
			if err := s.onProgress(ctx, event); err != nil {
				return nil, err
			}
		}
	}
	return wire.AppendVarint(nil, 1, boolWire(newRecord)), nil
}
