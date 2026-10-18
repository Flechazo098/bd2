package eventplay

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
)

func (s *Service) AttachProgress(fn func(ctx command.Context, condition, sub, count uint64) error) {
	s.onProgress = fn
}

func (s *Service) AttachRhythmResult(fn func(command.Context, events.RhythmResult) error) {
	s.onRhythmResult = fn
}
