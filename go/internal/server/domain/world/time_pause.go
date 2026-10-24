package world

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type fieldTimePause struct {
	Session        string `json:"session"`
	Pack           int    `json:"pack"`
	StartedMS      int64  `json:"started_ms"`
	AppliedSeconds int64  `json:"applied_seconds"`
}

func (s *Service) readFieldPause(ctx command.Context) (fieldTimePause, error) {
	var pause fieldTimePause
	raw, err := s.monsterStore.Load(ctx.State, "field_time_pause")
	if err != nil || raw == nil {
		return pause, err
	}
	if err := stateio.RequireExactJSONObject(raw, "session", "pack", "started_ms", "applied_seconds"); err != nil {
		return pause, err
	}
	if err := json.Unmarshal(raw, &pause); err != nil {
		return pause, err
	}
	if pause.Session == "" {
		if pause.Pack != 0 || pause.StartedMS != 0 || pause.AppliedSeconds != 0 {
			return pause, fmt.Errorf("world: invalid cleared field pause")
		}
	} else if pause.Pack <= 0 || pause.StartedMS <= 0 || pause.AppliedSeconds < 0 || pause.AppliedSeconds > math.MaxInt32 {
		return pause, fmt.Errorf("world: invalid field pause")
	}
	return pause, nil
}

func (s *Service) saveFieldPause(ctx command.Context, pause fieldTimePause) error {
	raw, err := json.Marshal(pause)
	if err != nil {
		return err
	}
	return s.monsterStore.Save(ctx.State, "field_time_pause", raw)
}

func (s *Service) advanceFieldPause(ctx command.Context, monsters *fieldMonsterSnapshot, finish bool) (int64, error) {
	pause, err := s.readFieldPause(ctx)
	if err != nil || pause.Session == "" {
		return 0, err
	}
	seconds := max(pause.AppliedSeconds, (max(s.monsterTime().UnixMilli()-pause.StartedMS, 0)+999)/1000)
	if seconds > math.MaxInt32 {
		return 0, fmt.Errorf("world: field pause exceeds protocol range")
	}
	if delta := (seconds - pause.AppliedSeconds) * 1000; delta > 0 {
		if s.monsterLoader == nil {
			return 0, fmt.Errorf("world: paused monster design unavailable")
		}
		designs, err := s.monsterLoader(pause.Pack)
		if err != nil {
			return 0, err
		}
		for _, design := range designs {
			if design.LifeSeconds == 0 {
				continue
			}
			key := monsterKey(pause.Pack, design.ID)
			state, exists := monsters.Monsters[key]
			if !exists {
				continue
			}
			if state.LifeEnd > math.MaxInt64-delta || state.Respawn > math.MaxInt64-delta {
				return 0, fmt.Errorf("world: paused monster deadline overflow")
			}
			if state.LifeEnd > 0 {
				state.LifeEnd += delta
			}
			if state.Respawn > 0 {
				state.Respawn += delta
			}
			monsters.Monsters[key] = state
		}
		if err := s.saveMonsterState(ctx, *monsters); err != nil {
			return 0, err
		}
	}
	sameSession := pause.Session == ctx.SessionID
	if finish || !sameSession {
		pause = fieldTimePause{}
	} else if seconds == pause.AppliedSeconds {
		return seconds, nil
	} else {
		pause.AppliedSeconds = seconds
	}
	if err := s.saveFieldPause(ctx, pause); err != nil {
		return 0, err
	}
	if !sameSession {
		return 0, nil
	}
	return seconds, nil
}

func (s *Service) ResumeFieldTime(ctx command.Context) error {
	if s.monsterStore == nil {
		return nil
	}
	monsters, err := s.readMonsterState(ctx)
	if err != nil {
		return err
	}
	_, err = s.advanceFieldPause(ctx, &monsters, true)
	return err
}

func (s *Service) handleTimePause(ctx command.Context, request []byte) (int, []byte, bool, error) {
	var seq, action uint64
	var seen [2]bool
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number < 1 || field.Number > 2 || field.Type != 0 || seen[field.Number-1] {
			return fmt.Errorf("%w: TimePause field %d wire type %d is unknown or repeated", ErrInvalidRequest, field.Number, field.Type)
		}
		seen[field.Number-1] = true
		value, _ := binary.Uvarint(field.Value)
		if field.Number == 1 {
			seq = value
		} else {
			action = value
		}
		return nil
	})
	if err != nil || seq == 0 || seq > math.MaxInt32 || action > 1 || ctx.SessionID == "" {
		return 100, nil, true, fmt.Errorf("%w: TimePause sequence %d action %d session %q: %v", ErrInvalidRequest, seq, action, ctx.SessionID, err)
	}
	if s.monsterStore == nil {
		return 100, nil, true, fmt.Errorf("world: field pause persistence unavailable")
	}
	pack, err := s.CurrentPackID(ctx)
	if err != nil {
		return 100, nil, true, err
	}
	monsters, err := s.readMonsterState(ctx)
	if err != nil {
		return 100, nil, true, err
	}
	if action == 1 {
		seconds, err := s.advanceFieldPause(ctx, &monsters, true)
		return 100, wire.AppendVarint(nil, 1, uint64(seconds)), true, err
	}
	pause, err := s.readFieldPause(ctx)
	if err != nil {
		return 100, nil, true, err
	}
	if pause.Session == ctx.SessionID && pause.Pack == pack {
		return 100, nil, true, nil
	}
	if _, err := s.advanceFieldPause(ctx, &monsters, true); err != nil {
		return 100, nil, true, err
	}
	if s.monsterLoader == nil {
		return 100, nil, true, fmt.Errorf("world: paused monster design unavailable")
	}
	designs, err := s.monsterLoader(pack)
	if err != nil {
		return 100, nil, true, err
	}
	for _, design := range designs {
		if _, exists := monsters.Monsters[monsterKey(pack, design.ID)]; exists {
			if _, err := s.monsterState(&monsters, pack, design); err != nil {
				return 100, nil, true, err
			}
		}
	}
	if err := s.saveMonsterState(ctx, monsters); err != nil {
		return 100, nil, true, err
	}
	err = s.saveFieldPause(ctx, fieldTimePause{Session: ctx.SessionID, Pack: pack, StartedMS: s.monsterTime().UnixMilli()})
	return 100, nil, true, err
}
