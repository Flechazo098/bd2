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
	if path == "/MiniGameRanking" || path == "/MiniGameUserRecordInfo" {
		uid = 0
		monster = 0
		stage = 0
	}
	if stage > 2 {
		return nil, fmt.Errorf("eventplay: invalid native stage type")
	}
	ranks, err := source.NativeRankings(ctx, uid, monster, stage)
	if err != nil {
		return nil, err
	}
	var out []byte
	if path == "/MiniGameRanking" || path == "/MiniGameUserRecordInfo" {
		b := summaryWire(&s.state, path == "/MiniGameUserRecordInfo")
		if err = wire.Walk(b, func(f wire.Field) error {
			if f.Number != 3 && f.Number != 4 {
				out = append(out, b[f.Start:f.End]...)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	for _, r := range ranks {
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
func nativeRankMember(u NativeRankMember) []byte {
	b := wire.AppendVarint(nil, 1, u.OwnerIndex)
	b = wire.AppendString(b, 2, u.UserID)
	return wire.AppendVarint(b, 3, u.CharacterID)
}
