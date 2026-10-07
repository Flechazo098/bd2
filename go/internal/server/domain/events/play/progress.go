package eventplay

import "bd2server/internal/server/domain/command"

func (s *Service) AttachProgress(fn func(ctx command.Context, condition, sub, count uint64) error) {
	s.onProgress = fn
}
