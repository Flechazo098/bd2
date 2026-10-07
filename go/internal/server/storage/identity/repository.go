package identitystore

import (
	"bd2server/internal/server/domain/identity"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

type transaction struct {
	store *Store
	tx    *sql.Tx
}

func (s *Store) Now() time.Time { return s.now() }
func (s *Store) Begin() (identity.Transaction, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return nil, e
	}
	return &transaction{store: s, tx: tx}, nil
}
func (t *transaction) Commit() error   { return t.tx.Commit() }
func (t *transaction) Rollback() error { return t.tx.Rollback() }
func changed(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, errors.New("auth: missing SQL result")
	}
	n, e := result.RowsAffected()
	if e != nil {
		return false, fmt.Errorf("auth: count affected rows: %w", e)
	}
	return n == 1, nil
}
func (s *Store) StartRecord(id, ticket string) (identity.StartRecord, error) {
	var r identity.StartRecord
	var hash []byte
	e := s.db.QueryRow(`SELECT start_hash,provider,status,expires_at FROM devices WHERE id=?`, id).Scan(&hash, &r.Provider, &r.Status, &r.Expires)
	r.Matches = subtle.ConstantTimeCompare(hash, s.digest("start-ticket", ticket)) == 1
	return r, e
}
func (s *Store) StartAuthorization(id, state, verifier, nonce string) (bool, error) {
	v, e := s.seal(id, "pkce", []byte(verifier))
	if e != nil {
		return false, e
	}
	n, e := s.seal(id, "nonce", []byte(nonce))
	if e != nil {
		return false, e
	}
	return changed(s.db.Exec(`UPDATE devices SET state_hash=?,verifier_cipher=?,nonce_cipher=?,start_hash=X'',status='authorizing' WHERE id=? AND status='created'`, s.digest("oauth-state", state), v, n, id))
}
func (s *Store) CancelAuthorization(provider, state string, now int64) (bool, error) {
	return changed(s.db.Exec(`UPDATE devices SET status='failed',error_code='provider_cancelled',state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL WHERE state_hash=? AND provider=? AND status='authorizing' AND expires_at>?`, s.digest("oauth-state", state), provider, now))
}
func (s *Store) AuthorizationRecord(state string) (identity.AuthorizationRecord, error) {
	var r identity.AuthorizationRecord
	e := s.db.QueryRow(`SELECT id,provider,status,verifier_cipher,nonce_cipher,expires_at FROM devices WHERE state_hash=?`, s.digest("oauth-state", state)).Scan(&r.ID, &r.Provider, &r.Status, &r.VerifierCipher, &r.NonceCipher, &r.Expires)
	return r, e
}
func (s *Store) AuthorizationSecrets(r identity.AuthorizationRecord) ([]byte, []byte, error) {
	v, e := s.open(r.ID, "pkce", r.VerifierCipher)
	if e != nil {
		return nil, nil, e
	}
	n, e := s.open(r.ID, "nonce", r.NonceCipher)
	if e != nil {
		clear(v)
		return nil, nil, e
	}
	return v, n, nil
}
func (s *Store) FailAuthorization(id string) error {
	_, e := s.db.Exec(`UPDATE devices SET status='failed',error_code='provider_rejected',state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL WHERE id=? AND status='authorizing'`, id)
	return e
}
func (t *transaction) CleanupExpired(now int64) error {
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM devices WHERE expires_at<=?`, []any{now}},
		{`DELETE FROM access_tokens WHERE expires_at<=? OR family_id IN (SELECT id FROM families WHERE expires_at<=?)`, []any{now, now}},
		{`DELETE FROM refresh_attempts WHERE expires_at<=? OR family_id IN (SELECT id FROM families WHERE expires_at<=?)`, []any{now, now}},
		{`DELETE FROM refresh_tokens WHERE family_id IN (SELECT id FROM families WHERE expires_at<=?)`, []any{now}},
		{`DELETE FROM families WHERE expires_at<=?`, []any{now}},
	}
	for _, q := range statements {
		if _, e := t.tx.Exec(q.query, q.args...); e != nil {
			return e
		}
	}
	return nil
}
func (t *transaction) PendingDevices(clientIP string, now int64) (int, error) {
	var n int
	e := t.tx.QueryRow(`SELECT COUNT(*) FROM devices WHERE client_hash=? AND status IN ('created','authorizing') AND expires_at>?`, t.store.digest("client-ip", clientIP), now).Scan(&n)
	return n, e
}
func (t *transaction) InsertDevice(r identity.DeviceInsert) error {
	_, e := t.tx.Exec(`INSERT INTO devices(id,client_hash,secret_hash,start_hash,provider,status,created_at,expires_at) VALUES(?,?,?,?,?,'created',?,?)`, r.ID, t.store.digest("client-ip", r.ClientIP), t.store.digest("device-secret", r.Secret), t.store.digest("start-ticket", r.StartTicket), r.Provider, r.Created, r.Expires)
	return e
}
func (t *transaction) Identity(id identity.ProviderIdentity) (identity.AccountRecord, bool, error) {
	var r identity.AccountRecord
	e := t.tx.QueryRow(`SELECT i.account_id,a.status FROM identities i JOIN accounts a ON a.id=i.account_id WHERE i.issuer=? AND i.subject_hash=?`, id.Issuer, t.store.identityDigest(id.Issuer, id.Subject)).Scan(&r.ID, &r.Status)
	if errors.Is(e, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, e == nil, e
}
func (t *transaction) AccountCount() (int, error) {
	var n int
	e := t.tx.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&n)
	return n, e
}
func (t *transaction) RejectDevice(id, provider, code string) (bool, error) {
	return changed(t.tx.Exec(`UPDATE devices SET status='failed',error_code=?,state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL WHERE id=? AND provider=? AND status='authorizing'`, code, id, provider))
}
func (t *transaction) InsertAccount(accountID, provider string, id identity.ProviderIdentity, now int64) error {
	if _, e := t.tx.Exec(`INSERT INTO accounts(id,status,created_at,last_login_at) VALUES(?,'active',?,?)`, accountID, now, now); e != nil {
		return e
	}
	if _, err := allocateGameProfile(t.tx, accountID); err != nil {
		return err
	}
	_, e := t.tx.Exec(`INSERT INTO identities(provider,issuer,subject_hash,account_id,created_at,last_login_at) VALUES(?,?,?,?,?,?)`, provider, id.Issuer, t.store.identityDigest(id.Issuer, id.Subject), accountID, now, now)
	return e
}
func (t *transaction) TouchIdentity(id identity.ProviderIdentity, now int64) error {
	_, e := t.tx.Exec(`UPDATE identities SET last_login_at=? WHERE issuer=? AND subject_hash=?`, now, id.Issuer, t.store.identityDigest(id.Issuer, id.Subject))
	return e
}
func (t *transaction) InsertFamily(r identity.FamilyInsert) error {
	_, e := t.tx.Exec(`INSERT INTO families(id,account_id,provider,created_at,expires_at) VALUES(?,?,?,?,?)`, r.ID, r.AccountID, r.Provider, r.Created, r.Expires)
	return e
}

type tokenPayload struct {
	Provider         string `json:"provider"`
	AccessToken      string `json:"access_token"`
	AccessExpiresIn  int64  `json:"access_expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

func tokenPayloadOf(r identity.TokenSet) tokenPayload {
	return tokenPayload{r.Provider, r.AccessToken, r.AccessExpiresIn, r.RefreshToken, r.RefreshExpiresIn}
}
func (r tokenPayload) tokens() identity.TokenSet {
	return identity.TokenSet{Provider: r.Provider, AccessToken: r.AccessToken, AccessExpiresIn: r.AccessExpiresIn, RefreshToken: r.RefreshToken, RefreshExpiresIn: r.RefreshExpiresIn}
}
func (t *transaction) CompleteDevice(id, provider string, result identity.TokenSet) (bool, error) {
	plain, e := json.Marshal(tokenPayloadOf(result))
	if e != nil {
		return false, e
	}
	sealed, e := t.store.seal(id, "result", plain)
	clear(plain)
	if e != nil {
		return false, e
	}
	return changed(t.tx.Exec(`UPDATE devices SET result_cipher=?,status='complete',state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL WHERE id=? AND provider=? AND status='authorizing'`, sealed, id, provider))
}
func (t *transaction) PollDevice(id, secret string) (identity.DeviceRecord, error) {
	var r identity.DeviceRecord
	var hash []byte
	e := t.tx.QueryRow(`SELECT secret_hash,status,COALESCE(result_cipher,X''),COALESCE(error_code,''),expires_at FROM devices WHERE id=?`, id).Scan(&hash, &r.Status, &r.ResultCipher, &r.ErrorCode, &r.Expires)
	r.Matches = subtle.ConstantTimeCompare(hash, t.store.digest("device-secret", secret)) == 1
	return r, e
}
func (s *Store) DecodeDeviceResult(id string, cipher []byte) (identity.TokenSet, error) {
	plain, e := s.open(id, "result", cipher)
	if e != nil {
		return identity.TokenSet{}, e
	}
	defer clear(plain)
	var r tokenPayload
	e = json.Unmarshal(plain, &r)
	return r.tokens(), e
}
func (t *transaction) ConsumeDevice(id string) (bool, error) {
	return changed(t.tx.Exec(`UPDATE devices SET result_cipher=NULL,status='consumed' WHERE id=? AND status='complete'`, id))
}
func (t *transaction) RefreshToken(token string) (identity.RefreshRecord, error) {
	var r identity.RefreshRecord
	var used, revoked sql.NullInt64
	e := t.tx.QueryRow(`SELECT r.family_id,f.account_id,f.provider,a.status,r.expires_at,f.expires_at,r.used_at,COALESCE(r.revoked_at,f.revoked_at) FROM refresh_tokens r JOIN families f ON f.id=r.family_id JOIN accounts a ON a.id=f.account_id WHERE r.token_hash=?`, t.store.digest("refresh-token", token)).Scan(&r.FamilyID, &r.AccountID, &r.Provider, &r.AccountStatus, &r.TokenExpires, &r.FamilyExpires, &used, &revoked)
	r.Used = used.Valid
	r.Revoked = revoked.Valid
	return r, e
}

type refreshPayload struct {
	Provider         string `json:"provider"`
	AccessToken      string `json:"access_token"`
	AccessExpiresAt  int64  `json:"access_expires_at"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresAt int64  `json:"refresh_expires_at"`
}

func refreshSealID(family string, hash []byte) string {
	return family + ":" + base64.RawURLEncoding.EncodeToString(hash)
}
func (t *transaction) RefreshAttempt(familyID, attemptID, requestToken string) (identity.RefreshAttempt, bool, error) {
	var r identity.RefreshAttempt
	var requestHash, cipher []byte
	hash := t.store.digest("refresh-attempt", attemptID)
	e := t.tx.QueryRow(`SELECT request_token_hash,result_cipher FROM refresh_attempts WHERE family_id=? AND attempt_hash=?`, familyID, hash).Scan(&requestHash, &cipher)
	if errors.Is(e, sql.ErrNoRows) {
		return r, false, nil
	}
	if e != nil {
		return r, false, e
	}
	r.Matches = subtle.ConstantTimeCompare(requestHash, t.store.digest("refresh-token", requestToken)) == 1
	if !r.Matches {
		return r, true, nil
	}
	plain, e := t.store.open(refreshSealID(familyID, hash), "result", cipher)
	if e != nil {
		return r, true, e
	}
	defer clear(plain)
	var p refreshPayload
	e = json.Unmarshal(plain, &p)
	r.Result = identity.StoredRefreshResult{Provider: p.Provider, AccessToken: p.AccessToken, AccessExpiresAt: p.AccessExpiresAt, RefreshToken: p.RefreshToken, RefreshExpiresAt: p.RefreshExpiresAt}
	return r, true, e
}
func (t *transaction) ConsumeRefreshToken(token string, now int64) (bool, error) {
	return changed(t.tx.Exec(`UPDATE refresh_tokens SET used_at=? WHERE token_hash=? AND used_at IS NULL AND revoked_at IS NULL`, now, t.store.digest("refresh-token", token)))
}
func (t *transaction) RevokeFamily(id string, now int64) (bool, error) {
	return changed(t.tx.Exec(`UPDATE families SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now, id))
}
func (t *transaction) DeleteFamilyAccess(id string) error {
	_, e := t.tx.Exec(`DELETE FROM access_tokens WHERE family_id=?`, id)
	return e
}
func (t *transaction) InsertAccess(token, familyID, accountID string, created, expires int64) error {
	_, e := t.tx.Exec(`INSERT INTO access_tokens(token_hash,family_id,account_id,created_at,expires_at) VALUES(?,?,?,?,?)`, t.store.digest("access-token", token), familyID, accountID, created, expires)
	return e
}
func (t *transaction) InsertRefresh(token, familyID string, created, expires int64) error {
	_, e := t.tx.Exec(`INSERT INTO refresh_tokens(token_hash,family_id,created_at,expires_at) VALUES(?,?,?,?)`, t.store.digest("refresh-token", token), familyID, created, expires)
	return e
}
func (t *transaction) InsertRefreshAttempt(familyID, attemptID, requestToken string, r identity.StoredRefreshResult, created, expires int64) error {
	hash := t.store.digest("refresh-attempt", attemptID)
	p := refreshPayload{r.Provider, r.AccessToken, r.AccessExpiresAt, r.RefreshToken, r.RefreshExpiresAt}
	plain, e := json.Marshal(p)
	if e != nil {
		return e
	}
	cipher, e := t.store.seal(refreshSealID(familyID, hash), "result", plain)
	clear(plain)
	if e != nil {
		return e
	}
	_, e = t.tx.Exec(`INSERT INTO refresh_attempts(family_id,attempt_hash,request_token_hash,result_cipher,created_at,expires_at) VALUES(?,?,?,?,?,?)`, familyID, hash, t.store.digest("refresh-token", requestToken), cipher, created, expires)
	return e
}

type rowScanner interface{ Scan(...any) error }

func accessRecord(row rowScanner) (identity.AccessRecord, error) {
	var r identity.AccessRecord
	var revoked sql.NullInt64
	e := row.Scan(&r.FamilyID, &r.AccountID, &r.AccountStatus, &r.Expires, &revoked)
	r.Revoked = revoked.Valid
	return r, e
}

const accessQuery = `SELECT t.family_id,t.account_id,a.status,t.expires_at,COALESCE(t.revoked_at,f.revoked_at) FROM access_tokens t JOIN families f ON f.id=t.family_id JOIN accounts a ON a.id=t.account_id WHERE t.token_hash=?`

func (s *Store) AccessToken(token string) (identity.AccessRecord, error) {
	return accessRecord(s.db.QueryRow(accessQuery, s.digest("access-token", token)))
}
func (t *transaction) AccessToken(token string) (identity.AccessRecord, error) {
	return accessRecord(t.tx.QueryRow(accessQuery, t.store.digest("access-token", token)))
}

func (s *Store) OwnerAccountID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='administrator_account_id'`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func (s *Store) AdministratorAccountID() (string, error) {
	return s.OwnerAccountID(context.Background())
}
func (t *transaction) SetAdministratorAccount(accountID string) error {
	_, err := t.tx.Exec(`INSERT INTO metadata(key,value) VALUES('administrator_account_id',?)`, accountID)
	return err
}

type GameProfile struct {
	AccountID  string
	OwnerIndex int64
	UserID     string
}

func allocateGameProfile(tx *sql.Tx, accountID string) (GameProfile, error) {
	var maximum int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(owner_index),0) FROM game_profiles`).Scan(&maximum); err != nil {
		return GameProfile{}, err
	}
	if maximum == math.MaxInt64 {
		return GameProfile{}, errors.New("auth: public game identity space exhausted")
	}
	index := maximum + 1
	userID := strconv.FormatInt(index, 10)
	// The imported owner's display identity may be decimal without matching its
	// numeric index. Reserved user IDs remain unavailable to subsequent players.
	for {
		var reserved int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM game_profiles WHERE user_id=?`, userID).Scan(&reserved); err != nil {
			return GameProfile{}, err
		}
		if reserved == 0 {
			break
		}
		if index == math.MaxInt64 {
			return GameProfile{}, errors.New("auth: public game identity space exhausted")
		}
		index++
		userID = strconv.FormatInt(index, 10)
	}
	if _, err := tx.Exec(`INSERT INTO game_profiles(account_id,owner_index,user_id) VALUES(?,?,?)`, accountID, index, userID); err != nil {
		return GameProfile{}, err
	}
	return GameProfile{AccountID: accountID, OwnerIndex: index, UserID: userID}, nil
}
func (s *Store) GameIdentity(ctx context.Context, accountID string) (GameProfile, error) {
	if accountID == "" {
		return GameProfile{}, errors.New("auth: public game identity requires account")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GameProfile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var profile GameProfile
	profile.AccountID = accountID
	err = tx.QueryRowContext(ctx, `SELECT owner_index,user_id FROM game_profiles WHERE account_id=?`, accountID).Scan(&profile.OwnerIndex, &profile.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		var owner string
		ownerErr := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='administrator_account_id'`).Scan(&owner)
		if ownerErr != nil {
			return GameProfile{}, ownerErr
		}
		if accountID == owner {
			return GameProfile{}, errors.New("auth: original owner's public game profile missing; offline repair required")
		}
		var active string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id=?`, accountID).Scan(&active); err != nil {
			return GameProfile{}, err
		}
		if active != "active" {
			return GameProfile{}, errors.New("auth: public game identity account inactive")
		}
		profile, err = allocateGameProfile(tx, accountID)
	}
	if err != nil {
		return GameProfile{}, err
	}
	if profile.OwnerIndex <= 0 || profile.UserID == "" {
		return GameProfile{}, errors.New("auth: public game identity invalid")
	}
	if err = tx.Commit(); err != nil {
		return GameProfile{}, err
	}
	return profile, nil
}
