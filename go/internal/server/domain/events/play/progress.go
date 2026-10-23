package eventplay

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/progression/conditions"
)

func (s *Service) AttachProgress(fn func(command.Context, conditions.Event) error) {
	s.onProgress = fn
}

func (s *Service) AttachRhythmResult(fn func(command.Context, events.RhythmResult) error) {
	s.onRhythmResult = fn
}
