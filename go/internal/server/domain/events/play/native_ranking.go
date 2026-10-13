package eventplay

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

type NativeRankMember struct {
	OwnerIndex  uint64
	UserID      string
	CharacterID uint64
}
type NativeRanking struct {
	InstanceID                         string
	EventUID, Monster, StageType, Rank uint64
	Score                              float64
	TopPercent                         float64
	Own                                bool
	Members                            []NativeRankMember
}
type NativeRankingSource interface {
	NativeRankings(command.Context, uint64, uint64, uint64) ([]NativeRanking, error)
}

func (s *Service) nativeRanking(ctx command.Context, path string, req []byte) ([]byte, error) {
	source, ok := s.rooms.(NativeRankingSource)
	if !ok {
		return nil, fmt.Errorf("eventplay: native ranking unavailable")
	}
	uid, monster, stage := num(req, 2), num(req, 3), num(req, 4)
	summary := path == "/MiniGameRanking" || path == "/MiniGameUserRecordInfo"
	current, err := s.currentRankingCalendars(ctx)
	if err != nil {
		return nil, err
	}
	if summary {
		uid = 0
		monster = 0
		stage = 0
	} else {
		calendar, err := s.calendar(uid, "Action")
		if err != nil {
			return nil, err
		}
		uid = calendar.UID
		if !s.validNativeRankScope(uid, monster, stage) {
			return nil, fmt.Errorf("eventplay: native ranking selection outside event group")
		}
	}
	if stage > 2 {
		return nil, fmt.Errorf("eventplay: invalid native stage type")
	}
	ranks, err := source.NativeRankings(ctx, uid, monster, stage)
	if err != nil {
		return nil, err
	}
	type selection struct{ monster, stage uint64 }
	latest := map[selection]uint64{}
	if summary {
		for _, row := range ranks {
			calendar, active := current[row.EventUID]
			if !active || !s.validNativeRankScope(row.EventUID, row.Monster, row.StageType) {
				continue
			}
			key := selection{row.Monster, row.StageType}
			old, exists := latest[key]
			previous := current[old]
			if !exists || calendar.Start > previous.Start || calendar.Start == previous.Start && calendar.UID > previous.UID {
				latest[key] = row.EventUID
			}
		}
	}
	var out []byte
	if path == "/MiniGameRanking" || path == "/MiniGameUserRecordInfo" {
		out, err = s.arcadeRanking(ctx, path, req)
		if err != nil {
			return nil, err
		}
	}
	for _, r := range ranks {
		if summary {
			if latest[selection{r.Monster, r.StageType}] != r.EventUID {
				continue
			}
		} else if !s.validNativeRankScope(r.EventUID, r.Monster, r.StageType) {
			continue
		}
		if len(r.Members) == 0 || r.Rank == 0 || r.Score <= 0 {
			continue
		}
		if path == "/MiniGameActionUserRecordInfo" || path == "/MiniGameUserRecordInfo" {
			if !r.Own {
				continue
			}
			b := wire.AppendVarint(nil, 1, r.Monster)
			b = wire.AppendDouble(b, 2, r.Score)
			b = wire.AppendDouble(b, 3, r.TopPercent)
			for _, u := range r.Members {
				b = wire.AppendBytes(b, 4, nativeRankMember(u))
			}
			field := 1
			if path == "/MiniGameUserRecordInfo" {
				field = 3
				if r.StageType == 2 {
					field = 4
				}
			}
			out = wire.AppendBytes(out, field, b)
			continue
		}
		if path == "/MiniGameRanking" {
			if r.Rank != 1 {
				continue
			}
			var b []byte
			for _, u := range r.Members {
				b = wire.AppendString(b, 1, u.UserID)
			}
			b = wire.AppendDouble(b, 2, r.Score)
			b = wire.AppendVarint(b, 3, r.Monster)
			field := 3
			if r.StageType == 2 {
				field = 4
			}
			out = wire.AppendBytes(out, field, b)
			continue
		}
		var b []byte
		field := 1
		ownField := 2
		if r.StageType == 2 {
			field = 3
			ownField = 4
			for _, u := range r.Members {
				b = wire.AppendBytes(b, 1, nativeRankMember(u))
			}
			b = wire.AppendVarint(b, 2, r.Rank)
			b = wire.AppendDouble(b, 3, r.Score)
		} else {
			u := r.Members[0]
			b = wire.AppendVarint(b, 1, u.OwnerIndex)
			b = wire.AppendString(b, 2, u.UserID)
			b = wire.AppendVarint(b, 3, r.Rank)
			b = wire.AppendDouble(b, 4, r.Score)
			b = wire.AppendVarint(b, 5, u.CharacterID)
		}
		out = wire.AppendBytes(out, field, b)
		if r.Own {
			out = wire.AppendBytes(out, ownField, b)
		}
	}
	return out, nil
}

func (s *Service) validNativeRankScope(uid, monster, stage uint64) bool {
	calendar, err := s.calendar(uid, "Action")
	if err != nil {
		return false
	}
	game, err := s.design.Row("PackEventMiniGameTable", 8, calendar.ID)
	if err != nil {
		return false
	}
	action, err := s.design.Row("ActionGameTable", 3, num(game, 4))
	if err != nil {
		return false
	}
	for _, row := range s.design.Rows("ActionGameStageTable", 3, num(action, 4)) {
		if (monster == 0 || num(row, 1) == monster) && (stage == 0 || num(row, 11) == stage) {
			return true
		}
	}
	return false
}
func nativeRankMember(u NativeRankMember) []byte {
	b := wire.AppendVarint(nil, 1, u.OwnerIndex)
	b = wire.AppendString(b, 2, u.UserID)
	return wire.AppendVarint(b, 3, u.CharacterID)
}
