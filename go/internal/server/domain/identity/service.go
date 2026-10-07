package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"
)

type FailureKind uint8

const (
	Unavailable FailureKind = iota
	Invalid
	Forbidden
	Expired
	Conflict
	Unauthorized
	TooManyPending
)

type Failure struct {
	Kind           FailureKind
	Message        string
	RefreshInvalid bool
}

func (e *Failure) Error() string                     { return e.Message }
func failure(kind FailureKind, message string) error { return &Failure{Kind: kind, Message: message} }

var (
	ErrUnauthorized = errors.New("auth: unauthorized")
	ErrConsumed     = errors.New("auth: consumed")
	ErrNotAllowed   = errors.New("auth: identity is not allowed")
)

type Service struct {
	config Config
	store  Repository
}

func New(config Config, store Repository) (*Service, error) {
	if store == nil {
		return nil, errors.New("auth: missing identity repository")
	}
	return &Service{config: config, store: store}, nil
}
func (s *Service) Now() time.Time { return s.store.Now() }
func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *Service) CreateDevice(provider, clientIP string) (CreatedDevice, error) {
	if _, ok := s.config.Providers[provider]; !ok {
		return CreatedDevice{}, failure(Invalid, "provider is not enabled")
	}
	id, e := randomToken(18)
	if e != nil {
		return CreatedDevice{}, e
	}
	secret, e := randomToken(32)
	if e != nil {
		return CreatedDevice{}, e
	}
	ticket, e := randomToken(32)
	if e != nil {
		return CreatedDevice{}, e
	}
	now := s.Now()
	tx, e := s.store.Begin()
	if e != nil {
		return CreatedDevice{}, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = tx.CleanupExpired(now.Unix()); e != nil {
		return CreatedDevice{}, e
	}
	pending, e := tx.PendingDevices(clientIP, now.Unix())
	if e != nil {
		return CreatedDevice{}, e
	}
	if pending >= 5 {
		return CreatedDevice{}, failure(TooManyPending, "too many pending login transactions")
	}
	if e = tx.InsertDevice(DeviceInsert{ID: id, ClientIP: clientIP, Secret: secret, StartTicket: ticket, Provider: provider, Created: now.Unix(), Expires: now.Add(s.config.DeviceTTL).Unix()}); e != nil {
		return CreatedDevice{}, e
	}
	if e = tx.Commit(); e != nil {
		return CreatedDevice{}, e
	}
	return CreatedDevice{ID: id, Secret: secret, StartTicket: ticket}, nil
}
func (s *Service) Start(provider, id, ticket string) (Authorization, error) {
	row, e := s.store.StartRecord(id, ticket)
	if e != nil || !row.Matches || row.Provider != provider || row.Status != "created" {
		return Authorization{}, failure(Forbidden, "invalid login transaction")
	}
	if s.Now().Unix() >= row.Expires {
		return Authorization{}, failure(Expired, "login transaction expired")
	}
	state, e := randomToken(32)
	if e != nil {
		return Authorization{}, e
	}
	verifier, e := randomToken(32)
	if e != nil {
		return Authorization{}, e
	}
	nonce, e := randomToken(24)
	if e != nil {
		return Authorization{}, e
	}
	updated, e := s.store.StartAuthorization(id, state, verifier, nonce)
	if e != nil {
		return Authorization{}, failure(Conflict, "could not start authorization")
	}
	if !updated {
		return Authorization{}, failure(Conflict, "could not start authorization")
	}
	return Authorization{ID: id, State: state, Verifier: verifier, Nonce: nonce}, nil
}
func (s *Service) CancelAuthorization(provider, state string) error {
	updated, e := s.store.CancelAuthorization(provider, state, s.Now().Unix())
	if e != nil {
		return failure(Unavailable, "authorization state unavailable")
	}
	if !updated {
		return failure(Forbidden, "invalid or expired authorization state")
	}
	return nil
}
func (s *Service) Authorization(provider, state string) (Authorization, error) {
	row, e := s.store.AuthorizationRecord(state)
	if e != nil || row.Provider != provider || row.Status != "authorizing" || s.Now().Unix() >= row.Expires {
		return Authorization{}, failure(Forbidden, "invalid or expired authorization state")
	}
	verifier, nonce, e := s.store.AuthorizationSecrets(row)
	if e != nil {
		return Authorization{}, failure(Unavailable, "authorization state unavailable")
	}
	defer clear(verifier)
	defer clear(nonce)
	return Authorization{ID: row.ID, State: state, Verifier: string(verifier), Nonce: string(nonce)}, nil
}
func (s *Service) RejectProvider(id string) { _ = s.store.FailAuthorization(id) }
func ValidateProviderIdentity(provider string, id ProviderIdentity) error {
	switch provider {
	case "discord":
		if id.Issuer != "https://discord.com" || len(id.Subject) == 0 || len(id.Subject) > 32 {
			return errors.New("auth: invalid Discord identity")
		}
		for _, c := range id.Subject {
			if c < '0' || c > '9' {
				return errors.New("auth: invalid Discord identity")
			}
		}
	case "google":
		if id.Issuer != "https://accounts.google.com" || len(id.Subject) == 0 || len(id.Subject) > 255 {
			return errors.New("auth: invalid Google identity")
		}
	default:
		return errors.New("auth: unsupported identity provider")
	}
	return nil
}
func (s *Service) ValidateGoogleClaims(issuer, audience, subject, nonce, expectedNonce string, expires int64) bool {
	validIssuer := issuer == "https://accounts.google.com" || issuer == "accounts.google.com"
	return validIssuer && audience == s.config.Providers["google"] && subject != "" && nonce == expectedNonce && s.Now().Unix() < expires
}
func (s *Service) CompleteDevice(deviceID, provider string, id ProviderIdentity) error {
	if e := ValidateProviderIdentity(provider, id); e != nil {
		return e
	}
	now := s.Now()
	tx, e := s.store.Begin()
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	account, found, e := tx.Identity(id)
	if e != nil {
		return e
	}
	reject := func() error {
		updated, e := tx.RejectDevice(deviceID, provider, "not_allowed")
		if e != nil {
			return e
		}
		if !updated {
			return ErrConsumed
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return ErrNotAllowed
	}
	if !found {
		count, e := tx.AccountCount()
		if e != nil {
			return e
		}
		account.ID, e = randomToken(18)
		if e != nil {
			return e
		}
		if e = tx.InsertAccount(account.ID, provider, id, now.Unix()); e != nil {
			return e
		}
		if count == 0 {
			if e = tx.SetAdministratorAccount(account.ID); e != nil {
				return e
			}
		}
		account.Status = "active"
	} else if e = tx.TouchIdentity(id, now.Unix()); e != nil {
		return e
	}
	if account.Status != "active" {
		return reject()
	}
	family, e := randomToken(18)
	if e != nil {
		return e
	}
	access, e := randomToken(32)
	if e != nil {
		return e
	}
	refresh, e := randomToken(32)
	if e != nil {
		return e
	}
	if e = tx.InsertFamily(FamilyInsert{ID: family, AccountID: account.ID, Provider: provider, Created: now.Unix(), Expires: now.Add(s.config.RefreshTTL).Unix()}); e != nil {
		return e
	}
	if e = tx.InsertAccess(access, family, account.ID, now.Unix(), now.Add(s.config.AccessTTL).Unix()); e != nil {
		return e
	}
	if e = tx.InsertRefresh(refresh, family, now.Unix(), now.Add(s.config.RefreshTTL).Unix()); e != nil {
		return e
	}
	tokens := TokenSet{Provider: provider, AccessToken: access, RefreshToken: refresh, AccessExpiresIn: int64(s.config.AccessTTL.Seconds()), RefreshExpiresIn: int64(s.config.RefreshTTL.Seconds())}
	updated, e := tx.CompleteDevice(deviceID, provider, tokens)
	if e != nil {
		return e
	}
	if !updated {
		return ErrConsumed
	}
	return tx.Commit()
}

type PollResult struct {
	Status, ErrorCode string
	Tokens            TokenSet
}

func (s *Service) Poll(id, secret string) (PollResult, error) {
	tx, e := s.store.Begin()
	if e != nil {
		return PollResult{}, failure(Unavailable, "login result unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	row, e := tx.PollDevice(id, secret)
	if e != nil || !row.Matches {
		return PollResult{}, failure(Forbidden, "invalid device transaction")
	}
	if s.Now().Unix() >= row.Expires {
		return PollResult{}, failure(Expired, "device transaction expired")
	}
	switch row.Status {
	case "created", "authorizing":
		return PollResult{Status: "pending"}, nil
	case "failed":
		return PollResult{Status: "failed", ErrorCode: row.ErrorCode}, nil
	case "complete":
		tokens, e := s.store.DecodeDeviceResult(id, row.ResultCipher)
		if e != nil {
			return PollResult{}, failure(Unavailable, "login result unavailable")
		}
		updated, e := tx.ConsumeDevice(id)
		if e != nil || !updated {
			return PollResult{}, failure(Expired, "login result already consumed")
		}
		if e = tx.Commit(); e != nil {
			return PollResult{}, failure(Unavailable, "login result unavailable")
		}
		return PollResult{Status: "complete", Tokens: tokens}, nil
	default:
		return PollResult{}, failure(Expired, "device transaction consumed")
	}
}
func ValidRefreshAttemptID(v string) bool {
	if len(v) < 16 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !valid {
			return false
		}
	}
	return true
}
func invalidRefresh(kind FailureKind, message string) error {
	return &Failure{Kind: kind, Message: message, RefreshInvalid: true}
}
func (s *Service) Refresh(token, attemptID string) (TokenSet, error) {
	if token == "" || !ValidRefreshAttemptID(attemptID) {
		return TokenSet{}, failure(Invalid, "refresh_token and valid attempt_id required")
	}
	now := s.Now()
	tx, e := s.store.Begin()
	if e != nil {
		return TokenSet{}, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = tx.CleanupExpired(now.Unix()); e != nil {
		return TokenSet{}, e
	}
	row, e := tx.RefreshToken(token)
	if e != nil || row.Revoked || row.AccountStatus != "active" || now.Unix() >= row.FamilyExpires {
		return TokenSet{}, invalidRefresh(Unauthorized, "refresh token invalid")
	}
	saved, found, e := tx.RefreshAttempt(row.FamilyID, attemptID, token)
	if e != nil {
		return TokenSet{}, e
	}
	if found {
		if !saved.Matches {
			return TokenSet{}, invalidRefresh(Conflict, "refresh attempt_id already belongs to another request")
		}
		if saved.Result.Provider == "" || saved.Result.AccessToken == "" || saved.Result.RefreshToken == "" {
			return TokenSet{}, failure(Unavailable, "refresh unavailable")
		}
		return saved.Result.Tokens(now.Unix()), nil
	}
	if now.Unix() >= row.TokenExpires {
		return TokenSet{}, invalidRefresh(Unauthorized, "refresh token expired")
	}
	if row.Used {
		if _, e = tx.RevokeFamily(row.FamilyID, now.Unix()); e != nil {
			return TokenSet{}, e
		}
		if e = tx.Commit(); e != nil {
			return TokenSet{}, e
		}
		return TokenSet{}, invalidRefresh(Unauthorized, "refresh token replayed")
	}
	access, e := randomToken(32)
	if e != nil {
		return TokenSet{}, e
	}
	refresh, e := randomToken(32)
	if e != nil {
		return TokenSet{}, e
	}
	updated, e := tx.ConsumeRefreshToken(token, now.Unix())
	if e != nil {
		return TokenSet{}, e
	}
	if !updated {
		return TokenSet{}, invalidRefresh(Unauthorized, "refresh token invalid")
	}
	if e = tx.DeleteFamilyAccess(row.FamilyID); e != nil {
		return TokenSet{}, e
	}
	accessExpiry := now.Add(s.config.AccessTTL).Unix()
	refreshExpiry := min(row.FamilyExpires, now.Add(s.config.RefreshTTL).Unix())
	if e = tx.InsertAccess(access, row.FamilyID, row.AccountID, now.Unix(), accessExpiry); e != nil {
		return TokenSet{}, e
	}
	if e = tx.InsertRefresh(refresh, row.FamilyID, now.Unix(), refreshExpiry); e != nil {
		return TokenSet{}, e
	}
	result := StoredRefreshResult{Provider: row.Provider, AccessToken: access, RefreshToken: refresh, AccessExpiresAt: accessExpiry, RefreshExpiresAt: refreshExpiry}
	if e = tx.InsertRefreshAttempt(row.FamilyID, attemptID, token, result, now.Unix(), refreshExpiry); e != nil {
		return TokenSet{}, e
	}
	if e = tx.Commit(); e != nil {
		return TokenSet{}, e
	}
	return result.Tokens(now.Unix()), nil
}
func (s *Service) Revoke(token string) error {
	now := s.Now().Unix()
	tx, e := s.store.Begin()
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	row, e := tx.AccessToken(token)
	if e != nil || row.AccountStatus != "active" || row.Revoked || now >= row.Expires {
		return failure(Unauthorized, "access token invalid")
	}
	updated, e := tx.RevokeFamily(row.FamilyID, now)
	if e != nil {
		return e
	}
	if !updated {
		return failure(Unauthorized, "access token invalid")
	}
	return tx.Commit()
}
func (s *Service) ValidateAccess(token string) (string, error) {
	if token == "" {
		return "", ErrUnauthorized
	}
	row, e := s.store.AccessToken(token)
	if e != nil || row.AccountStatus != "active" || row.Revoked || s.Now().Unix() >= row.Expires {
		return "", ErrUnauthorized
	}
	return row.AccountID, nil
}

func (s *Service) AuthorizeAdministrator(token string) (string, error) {
	accountID, err := s.ValidateAccess(token)
	if err != nil {
		return "", err
	}
	owner, err := s.store.AdministratorAccountID()
	if err != nil || owner == "" || owner != accountID {
		return "", ErrUnauthorized
	}
	return accountID, nil
}
