package auth

import (
	"errors"
	"fmt"
	"time"
)

// ConsoleLogin is a provider login started for the server's own web console.
// The device secret never leaves the server process.
type ConsoleLogin struct {
	ID       string
	Secret   string
	StartURL string
	Expires  time.Time
}

// Providers lists the enabled login providers in display order.
func (s *Service) Providers() []string { return s.config.Public().Providers }

// BeginConsoleLogin starts the same device transaction the game client uses.
func (s *Service) BeginConsoleLogin(provider, clientIP string) (ConsoleLogin, error) {
	if !s.limits.allow("create:"+clientIP, s.identity.Now(), time.Minute, 10) {
		return ConsoleLogin{}, fmt.Errorf("%w: console sign-ins from %s exceed 10 per minute", ErrRateLimited, clientIP)
	}
	device, err := s.identity.CreateDevice(provider, clientIP)
	if err != nil {
		return ConsoleLogin{}, err
	}
	return ConsoleLogin{ID: device.ID, Secret: device.Secret, StartURL: s.startURL(provider, device), Expires: s.identity.Now().Add(s.config.DeviceTTL)}, nil
}

// FinishConsoleLogin polls a console login. Once the provider completes it,
// the issued game tokens are revoked at once: the console keeps its own
// session and needs only the verified account.
func (s *Service) FinishConsoleLogin(id, secret string) (accountID string, complete bool, err error) {
	result, err := s.identity.Poll(id, secret)
	if err != nil {
		return "", false, err
	}
	switch result.Status {
	case "pending":
		return "", false, nil
	case "complete":
		accountID, err = s.identity.ValidateAccess(result.Tokens.AccessToken)
		revokeErr := s.identity.Revoke(result.Tokens.AccessToken)
		if err != nil {
			return "", false, err
		}
		return accountID, true, revokeErr
	default:
		return "", false, fmt.Errorf("%w: device login status %q", ErrLoginRejected, result.Status)
	}
}

// AdministratorAccountID returns the first registered game account.
func (s *Service) AdministratorAccountID() (string, error) {
	return s.identity.AdministratorAccountID()
}

var (
	ErrRateLimited   = errors.New("auth: too many login attempts")
	ErrLoginRejected = errors.New("auth: login was rejected")
)
