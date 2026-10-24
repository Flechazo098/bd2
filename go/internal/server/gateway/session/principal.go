package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bd2server/internal/server/gateway/transport"
)

type Principal struct {
	AccountID  string
	SessionID  string
	Generation uint64
}

func (s *Server) Principal(ctx context.Context, cookie string) (Principal, error) {
	game, err := s.authorize(cookie)
	if err != nil {
		return Principal{}, err
	}
	clear(game.key)
	p := Principal{AccountID: game.accountID, SessionID: game.id, Generation: game.generation}
	if err := s.ValidatePrincipal(ctx, p); err != nil {
		return Principal{}, err
	}
	return p, nil
}

func (s *Server) ValidatePrincipal(ctx context.Context, p Principal) error {
	if p.AccountID == "" || p.SessionID == "" || p.Generation == 0 {
		return fmt.Errorf("%w: principal %q session %q generation %d is incomplete", transport.ErrGameSessionExpired, p.AccountID, p.SessionID, p.Generation)
	}
	if !s.containsPrincipal(p) {
		return fmt.Errorf("%w: session %q of %s is not active", transport.ErrGameSessionExpired, p.SessionID, p.AccountID)
	}
	owner, release, err := s.router.Acquire(ctx, p.AccountID)
	if err != nil {
		return err
	}
	if release == nil {
		return errors.New("session principal runtime lease is missing")
	}
	defer release()
	if owner.Generation() != p.Generation || !s.containsPrincipal(p) {
		return fmt.Errorf("%w: session %q of %s generation %d is no longer current (player is at %d)", transport.ErrGameSessionExpired, p.SessionID, p.AccountID, p.Generation, owner.Generation())
	}
	return nil
}

func (s *Server) RecordActivity(p Principal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, game := range s.sessions {
		if game.accountID == p.AccountID && game.id == p.SessionID && game.generation == p.Generation && now.Before(game.expiresAt) {
			game.lastUsed = now
			return
		}
	}
}

const presenceActivityWindow = 90 * time.Second

// AccountOnline uses authenticated HTTP activity and native relay heartbeats;
// an idle session cookie alone does not keep a disconnected player online.
func (s *Server) AccountOnline(account string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, game := range s.sessions {
		if game.accountID == account && now.Before(game.expiresAt) && now.Sub(game.lastUsed) < presenceActivityWindow {
			return true
		}
	}
	return false
}

func (s *Server) containsPrincipal(p Principal) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, game := range s.sessions {
		if game.accountID == p.AccountID && game.id == p.SessionID && game.generation == p.Generation && now.Before(game.expiresAt) {
			return true
		}
	}
	return false
}
