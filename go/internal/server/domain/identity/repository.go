package identity

import "time"

type ProviderIdentity struct{ Issuer, Subject string }
type Config struct {
	Providers                        map[string]string
	AccessTTL, RefreshTTL, DeviceTTL time.Duration
}
type TokenSet struct {
	Provider, AccessToken, RefreshToken string
	AccessExpiresIn, RefreshExpiresIn   int64
}
type StoredRefreshResult struct {
	Provider, AccessToken, RefreshToken string
	AccessExpiresAt, RefreshExpiresAt   int64
}

func (r StoredRefreshResult) Tokens(now int64) TokenSet {
	return TokenSet{Provider: r.Provider, AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, AccessExpiresIn: max(r.AccessExpiresAt-now, 0), RefreshExpiresIn: max(r.RefreshExpiresAt-now, 0)}
}

type CreatedDevice struct{ ID, Secret, StartTicket string }
type Authorization struct{ ID, State, Verifier, Nonce string }
type StartRecord struct {
	Matches          bool
	Provider, Status string
	Expires          int64
}
type AuthorizationRecord struct {
	ID, Provider, Status        string
	Expires                     int64
	VerifierCipher, NonceCipher []byte
}
type AccountRecord struct{ ID, Status string }
type DeviceRecord struct {
	Matches           bool
	Status, ErrorCode string
	Expires           int64
	ResultCipher      []byte
}
type RefreshRecord struct {
	FamilyID, AccountID, Provider, AccountStatus string
	TokenExpires, FamilyExpires                  int64
	Used, Revoked                                bool
}
type RefreshAttempt struct {
	Matches bool
	Result  StoredRefreshResult
}
type AccessRecord struct {
	FamilyID, AccountID, AccountStatus string
	Expires                            int64
	Revoked                            bool
}
type DeviceInsert struct {
	ID, ClientIP, Secret, StartTicket, Provider string
	Created, Expires                            int64
}
type FamilyInsert struct {
	ID, AccountID, Provider string
	Created, Expires        int64
}

type Repository interface {
	Now() time.Time
	Begin() (Transaction, error)
	StartRecord(id, ticket string) (StartRecord, error)
	StartAuthorization(id, state, verifier, nonce string) (bool, error)
	CancelAuthorization(provider, state string, now int64) (bool, error)
	AuthorizationRecord(state string) (AuthorizationRecord, error)
	AuthorizationSecrets(AuthorizationRecord) (verifier, nonce []byte, err error)
	FailAuthorization(id string) error
	DecodeDeviceResult(id string, cipher []byte) (TokenSet, error)
	AccessToken(token string) (AccessRecord, error)
	AdministratorAccountID() (string, error)
}

type Transaction interface {
	CleanupExpired(now int64) error
	PendingDevices(clientIP string, now int64) (int, error)
	InsertDevice(DeviceInsert) error
	Identity(ProviderIdentity) (AccountRecord, bool, error)
	AccountCount() (int, error)
	SetAdministratorAccount(accountID string) error
	RejectDevice(id, provider, code string) (bool, error)
	InsertAccount(accountID, provider string, identity ProviderIdentity, now int64) error
	TouchIdentity(identity ProviderIdentity, now int64) error
	InsertFamily(FamilyInsert) error
	CompleteDevice(id, provider string, result TokenSet) (bool, error)
	PollDevice(id, secret string) (DeviceRecord, error)
	ConsumeDevice(id string) (bool, error)
	RefreshToken(token string) (RefreshRecord, error)
	RefreshAttempt(familyID, attemptID, requestToken string) (RefreshAttempt, bool, error)
	ConsumeRefreshToken(token string, now int64) (bool, error)
	RevokeFamily(id string, now int64) (bool, error)
	DeleteFamilyAccess(id string) error
	InsertAccess(token, familyID, accountID string, created, expires int64) error
	InsertRefresh(token, familyID string, created, expires int64) error
	InsertRefreshAttempt(familyID, attemptID, requestToken string, result StoredRefreshResult, created, expires int64) error
	AccessToken(token string) (AccessRecord, error)
	Commit() error
	Rollback() error
}
