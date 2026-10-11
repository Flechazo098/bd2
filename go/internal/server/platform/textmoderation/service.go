package textmoderation

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"bd2server/internal/server/design/gameconfig"
)

var (
	ErrRejected    = errors.New("text moderation: content rejected")
	ErrUnavailable = errors.New("text moderation: temporarily unavailable")
)

type Service struct {
	matcher *matcher
	remote  *remoteProvider
}

func New(config gameconfig.TextModerationConfig, baseDir string) (*Service, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	rules, err := loadRules(config.RuleFiles, baseDir)
	if err != nil {
		return nil, err
	}
	remote, err := newRemote(config.Provider)
	if err != nil {
		return nil, err
	}
	return &Service{matcher: compileMatcher(rules), remote: remote}, nil
}

func (s *Service) Check(ctx context.Context, scope, accountID, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || !gameconfig.ValidTextScope(scope) || !utf8.ValidString(text) {
		return fmt.Errorf("%w: invalid check input", ErrUnavailable)
	}
	key := matchingKey(text)
	if s.matcher.matches(scope, key) {
		return ErrRejected
	}
	if s.remote == nil || !s.remote.scopes[scope] {
		return nil
	}
	started := time.Now()
	err := s.remote.check(ctx, scope, accountID, text)
	if err == nil || errors.Is(err, ErrRejected) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.remote.logFailure(scope, started)
	if s.remote.failurePolicy == "allow" {
		return nil
	}
	return ErrUnavailable
}

func (s *Service) Close() error {
	if s != nil && s.remote != nil {
		s.remote.client.CloseIdleConnections()
	}
	return nil
}
