package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"bd2server/internal/server/authconfig"
	"bd2server/internal/server/wire"
)

type Service struct {
	config authconfig.Runtime
	store  *Store
	client *http.Client
	limits requestLimiter
}

type limitWindow struct {
	started time.Time
	count   int
}

type requestLimiter struct {
	mu        sync.Mutex
	windows   map[string]limitWindow
	lastSweep time.Time
}

type deviceResult struct {
	Provider         string `json:"provider"`
	AccessToken      string `json:"access_token"`
	AccessExpiresIn  int64  `json:"access_expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

type refreshAttemptResult struct {
	Provider         string `json:"provider"`
	AccessToken      string `json:"access_token"`
	AccessExpiresAt  int64  `json:"access_expires_at"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresAt int64  `json:"refresh_expires_at"`
}

func New(config authconfig.Runtime, store *Store) (*Service, error) {
	if config.Mode != "oauth" || store == nil {
		return nil, errors.New("auth: OAuth service requires oauth configuration and store")
	}
	// Store.Open has already derived its purpose-specific keys. Do not retain
	// the environment master key in the long-lived HTTP service configuration.
	clear(config.MasterKey)
	config.MasterKey = nil
	return &Service{config: config, store: store, client: &http.Client{Timeout: 15 * time.Second}, limits: requestLimiter{windows: make(map[string]limitWindow)}}, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/device", s.createDevice)
	mux.HandleFunc("GET /auth/{provider}/start", s.start)
	mux.HandleFunc("GET /auth/{provider}/callback", s.callback)
	mux.HandleFunc("POST /auth/device/{id}/poll", s.poll)
	mux.HandleFunc("POST /auth/session/refresh", s.refresh)
	mux.HandleFunc("POST /auth/session/revoke", s.revoke)
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer func() { _ = r.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(r.Body, 16<<10+1))
	if err != nil || len(data) > 16<<10 {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Service) createDevice(w http.ResponseWriter, r *http.Request) {
	clientIP := remoteIP(r.RemoteAddr)
	if !s.limits.allow("create:"+clientIP, s.store.now(), time.Minute, 10) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	var request struct {
		Provider string `json:"provider"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if _, ok := s.config.Providers[request.Provider]; !ok {
		http.Error(w, "provider is not enabled", http.StatusBadRequest)
		return
	}
	id, err := randomToken(18)
	if err != nil {
		http.Error(w, "could not create transaction", http.StatusInternalServerError)
		return
	}
	secret, err := randomToken(32)
	if err != nil {
		http.Error(w, "could not create transaction", http.StatusInternalServerError)
		return
	}
	startTicket, err := randomToken(32)
	if err != nil {
		http.Error(w, "could not create transaction", http.StatusInternalServerError)
		return
	}
	now := s.store.now()
	clientHash := s.store.digest("client-ip", clientIP)
	tx, err := s.store.db.Begin()
	if err != nil {
		http.Error(w, "could not create transaction", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := cleanupExpired(tx, now.Unix()); err != nil {
		http.Error(w, "could not create transaction", http.StatusInternalServerError)
		return
	}
	var pending int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM devices WHERE client_hash=? AND status IN ('created','authorizing') AND expires_at>?`, clientHash, now.Unix()).Scan(&pending); err != nil {
		http.Error(w, "could not create transaction", http.StatusInternalServerError)
		return
	}
	if pending >= 5 {
		w.Header().Set("Retry-After", strconv.FormatInt(int64(s.config.DeviceTTL.Seconds()), 10))
		http.Error(w, "too many pending login transactions", http.StatusTooManyRequests)
		return
	}
	_, err = tx.Exec(`INSERT INTO devices(id,client_hash,secret_hash,start_hash,provider,status,created_at,expires_at) VALUES(?,?,?,?,?,'created',?,?)`, id, clientHash, s.store.digest("device-secret", secret), s.store.digest("start-ticket", startTicket), request.Provider, now.Unix(), now.Add(s.config.DeviceTTL).Unix())
	if err != nil {
		http.Error(w, "could not create transaction", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "could not create transaction", http.StatusInternalServerError)
		return
	}
	start := *s.config.PublicURLParsed
	start.Path = "/auth/" + request.Provider + "/start"
	query := start.Query()
	query.Set("transaction_id", id)
	query.Set("ticket", startTicket)
	start.RawQuery = query.Encode()
	writeJSON(w, http.StatusCreated, map[string]any{"transaction_id": id, "device_secret": secret, "start_url": start.String(), "expires_in": int64(s.config.DeviceTTL.Seconds()), "poll_interval": 2})
}

func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if _, ok := s.config.Providers[provider]; !ok {
		http.Error(w, "provider is not enabled", http.StatusNotFound)
		return
	}
	id, ticket := r.URL.Query().Get("transaction_id"), r.URL.Query().Get("ticket")
	var storedHash []byte
	var storedProvider, status string
	var expires int64
	err := s.store.db.QueryRow(`SELECT start_hash,provider,status,expires_at FROM devices WHERE id=?`, id).Scan(&storedHash, &storedProvider, &status, &expires)
	if err != nil || subtle.ConstantTimeCompare(storedHash, s.store.digest("start-ticket", ticket)) != 1 || storedProvider != provider || status != "created" {
		http.Error(w, "invalid login transaction", http.StatusForbidden)
		return
	}
	if s.store.now().Unix() >= expires {
		http.Error(w, "login transaction expired", http.StatusGone)
		return
	}
	state, err := randomToken(32)
	if err != nil {
		http.Error(w, "could not start authorization", http.StatusInternalServerError)
		return
	}
	verifier, err := randomToken(32)
	if err != nil {
		http.Error(w, "could not start authorization", http.StatusInternalServerError)
		return
	}
	nonce, err := randomToken(24)
	if err != nil {
		http.Error(w, "could not start authorization", http.StatusInternalServerError)
		return
	}
	verifierCipher, err := s.store.seal(id, "pkce", []byte(verifier))
	if err != nil {
		http.Error(w, "could not start authorization", http.StatusInternalServerError)
		return
	}
	nonceCipher, err := s.store.seal(id, "nonce", []byte(nonce))
	if err != nil {
		http.Error(w, "could not start authorization", http.StatusInternalServerError)
		return
	}
	result, err := s.store.db.Exec(`UPDATE devices SET state_hash=?,verifier_cipher=?,nonce_cipher=?,start_hash=X'',status='authorizing' WHERE id=? AND status='created'`, s.store.digest("oauth-state", state), verifierCipher, nonceCipher, id)
	count, affectedErr := rowsAffected(result)
	if err != nil || affectedErr != nil || count != 1 {
		http.Error(w, "could not start authorization", http.StatusConflict)
		return
	}
	redirect := s.redirectURL(provider)
	challenge := sha256.Sum256([]byte(verifier))
	values := url.Values{"client_id": {s.config.Providers[provider].ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {providerScope(provider)}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	if provider == "google" {
		values.Set("nonce", nonce)
	}
	http.Redirect(w, r, providerAuthorizeURL(provider)+"?"+values.Encode(), http.StatusFound)
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	provider, state, code := r.PathValue("provider"), r.URL.Query().Get("state"), r.URL.Query().Get("code")
	if _, ok := s.config.Providers[provider]; !ok {
		http.Error(w, "provider is not enabled", http.StatusNotFound)
		return
	}
	if state == "" {
		http.Error(w, "authorization was not completed", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("error") != "" {
		result, err := s.store.db.Exec(`UPDATE devices SET status='failed',error_code='provider_cancelled',state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL
			WHERE state_hash=? AND provider=? AND status='authorizing' AND expires_at>?`, s.store.digest("oauth-state", state), provider, s.store.now().Unix())
		if err != nil {
			http.Error(w, "authorization state unavailable", http.StatusInternalServerError)
			return
		}
		if count, err := rowsAffected(result); err != nil || count != 1 {
			http.Error(w, "invalid or expired authorization state", http.StatusForbidden)
			return
		}
		http.Error(w, "authorization was cancelled", http.StatusBadRequest)
		return
	}
	if code == "" {
		http.Error(w, "authorization was not completed", http.StatusBadRequest)
		return
	}
	var id, storedProvider, status string
	var verifierCipher, nonceCipher []byte
	var expires int64
	err := s.store.db.QueryRow(`SELECT id,provider,status,verifier_cipher,nonce_cipher,expires_at FROM devices WHERE state_hash=?`, s.store.digest("oauth-state", state)).Scan(&id, &storedProvider, &status, &verifierCipher, &nonceCipher, &expires)
	if err != nil || provider != storedProvider || status != "authorizing" || s.store.now().Unix() >= expires {
		http.Error(w, "invalid or expired authorization state", http.StatusForbidden)
		return
	}
	verifier, err := s.store.open(id, "pkce", verifierCipher)
	if err != nil {
		http.Error(w, "authorization state unavailable", http.StatusInternalServerError)
		return
	}
	nonce, err := s.store.open(id, "nonce", nonceCipher)
	if err != nil {
		http.Error(w, "authorization state unavailable", http.StatusInternalServerError)
		return
	}
	identity, err := s.exchangeIdentity(r.Context(), provider, code, string(verifier), string(nonce))
	clear(verifier)
	clear(nonce)
	if err != nil {
		_, _ = s.store.db.Exec(`UPDATE devices SET status='failed',error_code='provider_rejected',state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL WHERE id=? AND status='authorizing'`, id)
		var failure *providerFailure
		if errors.As(err, &failure) {
			slog.Warn("OAuth provider authorization failed",
				"provider", failure.Provider,
				"stage", failure.Stage,
				"reason", failure.Reason,
				"http_status", failure.HTTPStatus,
				"oauth_error", failure.OAuthError)
			if failure.OAuthError == "invalid_client" {
				http.Error(w, "server OAuth configuration is invalid; contact the server administrator", http.StatusBadGateway)
				return
			}
		} else {
			slog.Warn("OAuth provider authorization failed", "provider", provider, "reason", "internal_error")
		}
		http.Error(w, "provider authorization failed", http.StatusBadGateway)
		return
	}
	if err := s.completeDevice(id, provider, identity); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, ErrNotAllowed) {
			code = http.StatusForbidden
		} else if errors.Is(err, ErrConsumed) {
			code = http.StatusConflict
		}
		http.Error(w, "authorization could not be completed", code)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>BD2 login</title><p>Login complete. You can return to the game.</p>`)
}

type providerIdentity struct{ issuer, subject string }

type providerFailure struct {
	Provider   string
	Stage      string
	Reason     string
	HTTPStatus int
	OAuthError string
}

func (e *providerFailure) Error() string {
	return fmt.Sprintf("provider=%s stage=%s reason=%s status=%d oauth_error=%s", e.Provider, e.Stage, e.Reason, e.HTTPStatus, e.OAuthError)
}

func networkProviderFailure(ctx context.Context, provider, stage string, err error) error {
	reason := "network_error"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		reason = "timeout"
	} else if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		reason = "cancelled"
	} else {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			reason = "timeout"
		}
	}
	return &providerFailure{Provider: provider, Stage: stage, Reason: reason}
}

func rejectedProviderFailure(provider, stage string, response *http.Response) error {
	failure := &providerFailure{Provider: provider, Stage: stage, Reason: "http_rejected", HTTPStatus: response.StatusCode}
	var body struct {
		Error string `json:"error"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<10))
	if decoder.Decode(&body) == nil {
		failure.OAuthError = safeOAuthError(body.Error)
	}
	return failure
}

func invalidProviderResponse(provider, stage string) error {
	return &providerFailure{Provider: provider, Stage: stage, Reason: "invalid_response", HTTPStatus: http.StatusOK}
}

func safeOAuthError(value string) string {
	switch value {
	case "invalid_request", "invalid_client", "invalid_grant", "unauthorized_client",
		"unsupported_grant_type", "invalid_scope", "access_denied", "server_error", "temporarily_unavailable":
		return value
	default:
		return "unknown"
	}
}

func (s *Service) exchangeIdentity(ctx context.Context, provider, code, verifier, nonce string) (providerIdentity, error) {
	values := url.Values{"client_id": {s.config.Providers[provider].ClientID}, "client_secret": {s.config.ProviderSecrets[provider]}, "grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.redirectURL(provider)}, "code_verifier": {verifier}}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, providerTokenURL(provider), strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		return providerIdentity{}, networkProviderFailure(ctx, provider, "token_exchange", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return providerIdentity{}, rejectedProviderFailure(provider, "token_exchange", response)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := decodeProviderJSON(response.Body, &token); err != nil || token.AccessToken == "" {
		return providerIdentity{}, invalidProviderResponse(provider, "token_exchange")
	}
	if provider == "google" {
		if token.IDToken == "" {
			return providerIdentity{}, invalidProviderResponse(provider, "token_exchange")
		}
		identity, err := s.verifyGoogleIDToken(ctx, token.IDToken, nonce)
		token.AccessToken, token.IDToken = "", ""
		return identity, err
	}
	userinfo, _ := http.NewRequestWithContext(ctx, http.MethodGet, providerUserURL(provider), nil)
	userinfo.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err = s.client.Do(userinfo)
	token.AccessToken = ""
	if err != nil {
		return providerIdentity{}, networkProviderFailure(ctx, provider, "userinfo", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return providerIdentity{}, rejectedProviderFailure(provider, "userinfo", response)
	}
	var user struct {
		ID  string `json:"id"`
		Sub string `json:"sub"`
	}
	if err := decodeProviderJSON(response.Body, &user); err != nil {
		return providerIdentity{}, invalidProviderResponse(provider, "userinfo")
	}
	if provider == "discord" && user.ID != "" {
		return providerIdentity{issuer: "https://discord.com", subject: user.ID}, nil
	}
	return providerIdentity{}, invalidProviderResponse(provider, "userinfo")
}

// verifyGoogleIDToken delegates signature and standard-claim verification to
// Google's HTTPS tokeninfo endpoint, then independently verifies this server's
// audience, nonce and expiry. The raw ID token is never persisted or logged.
func (s *Service) verifyGoogleIDToken(ctx context.Context, idToken, nonce string) (providerIdentity, error) {
	endpoint := "https://oauth2.googleapis.com/tokeninfo?id_token=" + url.QueryEscape(idToken)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	response, err := s.client.Do(request)
	if err != nil {
		return providerIdentity{}, networkProviderFailure(ctx, "google", "id_token_verify", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return providerIdentity{}, rejectedProviderFailure("google", "id_token_verify", response)
	}
	var claims struct {
		Issuer   string `json:"iss"`
		Audience string `json:"aud"`
		Subject  string `json:"sub"`
		Nonce    string `json:"nonce"`
		Expires  string `json:"exp"`
	}
	if err := decodeProviderJSON(response.Body, &claims); err != nil {
		return providerIdentity{}, invalidProviderResponse("google", "id_token_verify")
	}
	expires, err := strconv.ParseInt(claims.Expires, 10, 64)
	validIssuer := claims.Issuer == "https://accounts.google.com" || claims.Issuer == "accounts.google.com"
	if err != nil || !validIssuer || claims.Audience != s.config.Providers["google"].ClientID || claims.Subject == "" || claims.Nonce != nonce || s.store.now().Unix() >= expires {
		return providerIdentity{}, &providerFailure{Provider: "google", Stage: "id_token_verify", Reason: "invalid_claims"}
	}
	return providerIdentity{issuer: "https://accounts.google.com", subject: claims.Subject}, nil
}

func decodeProviderJSON(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("auth: provider response is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("auth: provider response has trailing JSON")
	}
	return nil
}

func (s *Service) completeDevice(deviceID, provider string, identity providerIdentity) error {
	if err := validateProviderIdentity(provider, identity); err != nil {
		return err
	}
	now := s.store.now()
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var accountID, status string
	subjectHash := s.store.identityDigest(identity.issuer, identity.subject)
	err = tx.QueryRow(`SELECT i.account_id,a.status FROM identities i JOIN accounts a ON a.id=i.account_id WHERE i.issuer=? AND i.subject_hash=?`, identity.issuer, subjectHash).Scan(&accountID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			result, err := tx.Exec(`UPDATE devices SET status='failed',error_code='not_allowed',state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL WHERE id=? AND provider=? AND status='authorizing'`, deviceID, provider)
			if err != nil {
				return err
			}
			if count, err := rowsAffected(result); err != nil {
				return err
			} else if count != 1 {
				return ErrConsumed
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			return ErrNotAllowed
		}
		accountID, err = randomToken(18)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO accounts(id,status,created_at,last_login_at) VALUES(?,'active',?,?)`, accountID, now.Unix(), now.Unix()); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO identities(provider,issuer,subject_hash,account_id,created_at,last_login_at) VALUES(?,?,?,?,?,?)`, provider, identity.issuer, subjectHash, accountID, now.Unix(), now.Unix()); err != nil {
			return err
		}
		status = "active"
	} else if err != nil {
		return err
	} else {
		if _, err = tx.Exec(`UPDATE identities SET last_login_at=? WHERE issuer=? AND subject_hash=?`, now.Unix(), identity.issuer, subjectHash); err != nil {
			return err
		}
	}
	if status != "active" {
		result, err := tx.Exec(`UPDATE devices SET status='failed',error_code='not_allowed',state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL WHERE id=? AND provider=? AND status='authorizing'`, deviceID, provider)
		if err != nil {
			return err
		}
		if count, err := rowsAffected(result); err != nil {
			return err
		} else if count != 1 {
			return ErrConsumed
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrNotAllowed
	}
	result, familyID, err := s.issueTokens(tx, accountID, provider, now)
	if err != nil {
		return err
	}
	_ = familyID
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	sealed, err := s.store.seal(deviceID, "result", payload)
	clear(payload)
	if err != nil {
		return err
	}
	update, err := tx.Exec(`UPDATE devices SET result_cipher=?,status='complete',state_hash=NULL,verifier_cipher=NULL,nonce_cipher=NULL WHERE id=? AND provider=? AND status='authorizing'`, sealed, deviceID, provider)
	if err != nil {
		return err
	}
	if count, err := rowsAffected(update); err != nil {
		return err
	} else if count != 1 {
		return ErrConsumed
	}
	return tx.Commit()
}

func validateProviderIdentity(provider string, identity providerIdentity) error {
	switch provider {
	case "discord":
		if identity.issuer != "https://discord.com" || len(identity.subject) == 0 || len(identity.subject) > 32 {
			return errors.New("auth: invalid Discord identity")
		}
		for _, digit := range identity.subject {
			if digit < '0' || digit > '9' {
				return errors.New("auth: invalid Discord identity")
			}
		}
	case "google":
		if identity.issuer != "https://accounts.google.com" || len(identity.subject) == 0 || len(identity.subject) > 255 {
			return errors.New("auth: invalid Google identity")
		}
	default:
		return errors.New("auth: unsupported identity provider")
	}
	return nil
}

func (s *Service) issueTokens(tx *sql.Tx, accountID, provider string, now time.Time) (deviceResult, string, error) {
	familyID, err := randomToken(18)
	if err != nil {
		return deviceResult{}, "", err
	}
	access, err := randomToken(32)
	if err != nil {
		return deviceResult{}, "", err
	}
	refresh, err := randomToken(32)
	if err != nil {
		return deviceResult{}, "", err
	}
	if _, err := tx.Exec(`INSERT INTO families(id,account_id,provider,created_at,expires_at) VALUES(?,?,?,?,?)`, familyID, accountID, provider, now.Unix(), now.Add(s.config.RefreshTTL).Unix()); err != nil {
		return deviceResult{}, "", err
	}
	if _, err := tx.Exec(`INSERT INTO access_tokens(token_hash,family_id,account_id,created_at,expires_at) VALUES(?,?,?,?,?)`, s.store.digest("access-token", access), familyID, accountID, now.Unix(), now.Add(s.config.AccessTTL).Unix()); err != nil {
		return deviceResult{}, "", err
	}
	if _, err := tx.Exec(`INSERT INTO refresh_tokens(token_hash,family_id,created_at,expires_at) VALUES(?,?,?,?)`, s.store.digest("refresh-token", refresh), familyID, now.Unix(), now.Add(s.config.RefreshTTL).Unix()); err != nil {
		return deviceResult{}, "", err
	}
	return deviceResult{Provider: provider, AccessToken: access, AccessExpiresIn: int64(s.config.AccessTTL.Seconds()), RefreshToken: refresh, RefreshExpiresIn: int64(s.config.RefreshTTL.Seconds())}, familyID, nil
}

func (s *Service) poll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.limits.allow("poll:"+remoteIP(r.RemoteAddr)+":"+id, s.store.now(), time.Minute, 60) {
		w.Header().Set("Retry-After", "2")
		http.Error(w, "poll rate exceeded", http.StatusTooManyRequests)
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Device ") {
		http.Error(w, "invalid device transaction", http.StatusForbidden)
		return
	}
	secret := strings.TrimPrefix(authorization, "Device ")
	tx, err := s.store.db.Begin()
	if err != nil {
		http.Error(w, "login result unavailable", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var storedHash, sealed []byte
	var status, errorCode string
	var expires int64
	err = tx.QueryRow(`SELECT secret_hash,status,COALESCE(result_cipher,X''),COALESCE(error_code,''),expires_at FROM devices WHERE id=?`, id).Scan(&storedHash, &status, &sealed, &errorCode, &expires)
	if err != nil || subtle.ConstantTimeCompare(storedHash, s.store.digest("device-secret", secret)) != 1 {
		http.Error(w, "invalid device transaction", http.StatusForbidden)
		return
	}
	if s.store.now().Unix() >= expires {
		http.Error(w, "device transaction expired", http.StatusGone)
		return
	}
	switch status {
	case "created", "authorizing":
		_ = tx.Rollback()
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "retry_after": 2})
	case "failed":
		_ = tx.Rollback()
		writeJSON(w, http.StatusForbidden, map[string]string{"status": "failed", "error": errorCode})
	case "complete":
		plain, err := s.store.open(id, "result", sealed)
		if err != nil {
			http.Error(w, "login result unavailable", http.StatusInternalServerError)
			return
		}
		result, err := tx.Exec(`UPDATE devices SET result_cipher=NULL,status='consumed' WHERE id=? AND status='complete'`, id)
		count, affectedErr := rowsAffected(result)
		if err != nil || affectedErr != nil || count != 1 {
			_ = tx.Rollback()
			clear(plain)
			http.Error(w, "login result already consumed", http.StatusGone)
			return
		}
		if err := tx.Commit(); err != nil {
			clear(plain)
			http.Error(w, "login result unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(plain)
		clear(plain)
	default:
		_ = tx.Rollback()
		http.Error(w, "device transaction consumed", http.StatusGone)
	}
}

func (l *requestLimiter) allow(key string, now time.Time, duration time.Duration, maximum int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = make(map[string]limitWindow)
	}
	if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= time.Minute {
		for candidate, window := range l.windows {
			if now.Sub(window.started) >= duration {
				delete(l.windows, candidate)
			}
		}
		l.lastSweep = now
	}
	window, exists := l.windows[key]
	if !exists || now.Sub(window.started) >= duration {
		if !exists && len(l.windows) >= 4096 {
			return false
		}
		l.windows[key] = limitWindow{started: now, count: 1}
		return true
	}
	if window.count >= maximum {
		return false
	}
	window.count++
	l.windows[key] = window
	return true
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
}

func cleanupExpired(tx *sql.Tx, now int64) error {
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM devices WHERE expires_at<=?`, []any{now}},
		{`DELETE FROM access_tokens WHERE expires_at<=? OR family_id IN (SELECT id FROM families WHERE expires_at<=?)`, []any{now, now}},
		{`DELETE FROM refresh_attempts WHERE expires_at<=? OR family_id IN (SELECT id FROM families WHERE expires_at<=?)`, []any{now, now}},
		// Used refresh rows remain until their family expires so their reuse can
		// still revoke every credential in that family.
		{`DELETE FROM refresh_tokens WHERE family_id IN (SELECT id FROM families WHERE expires_at<=?)`, []any{now}},
		{`DELETE FROM families WHERE expires_at<=?`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refresh_token"`
		AttemptID    string `json:"attempt_id"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.RefreshToken == "" || !validRefreshAttemptID(request.AttemptID) {
		http.Error(w, "refresh_token and valid attempt_id required", http.StatusBadRequest)
		return
	}
	now := s.store.now()
	requestTokenHash := s.store.digest("refresh-token", request.RefreshToken)
	attemptHash := s.store.digest("refresh-attempt", request.AttemptID)
	tx, err := s.store.db.Begin()
	if err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := cleanupExpired(tx, now.Unix()); err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	var familyID, accountID, provider, accountStatus string
	var tokenExpires, familyExpires int64
	var usedAt, revokedAt sql.NullInt64
	err = tx.QueryRow(`SELECT r.family_id,f.account_id,f.provider,a.status,r.expires_at,f.expires_at,r.used_at,COALESCE(r.revoked_at,f.revoked_at) FROM refresh_tokens r JOIN families f ON f.id=r.family_id JOIN accounts a ON a.id=f.account_id WHERE r.token_hash=?`, requestTokenHash).Scan(&familyID, &accountID, &provider, &accountStatus, &tokenExpires, &familyExpires, &usedAt, &revokedAt)
	if err != nil || revokedAt.Valid || accountStatus != "active" || now.Unix() >= familyExpires {
		w.Header().Set("X-BD2-Refresh-Invalid", "1")
		http.Error(w, "refresh token invalid", http.StatusUnauthorized)
		return
	}
	var savedRequestHash, resultCipher []byte
	err = tx.QueryRow(`SELECT request_token_hash,result_cipher FROM refresh_attempts WHERE family_id=? AND attempt_hash=?`, familyID, attemptHash).Scan(&savedRequestHash, &resultCipher)
	if err == nil {
		if subtle.ConstantTimeCompare(savedRequestHash, requestTokenHash) != 1 {
			w.Header().Set("X-BD2-Refresh-Invalid", "1")
			http.Error(w, "refresh attempt_id already belongs to another request", http.StatusConflict)
			return
		}
		plain, openErr := s.store.open(refreshAttemptSealID(familyID, attemptHash), "result", resultCipher)
		if openErr != nil {
			http.Error(w, "refresh unavailable", http.StatusInternalServerError)
			return
		}
		var saved refreshAttemptResult
		decodeErr := json.Unmarshal(plain, &saved)
		clear(plain)
		if decodeErr != nil || saved.Provider == "" || saved.AccessToken == "" || saved.RefreshToken == "" {
			http.Error(w, "refresh unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, saved.deviceResult(now.Unix()))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	if now.Unix() >= tokenExpires {
		w.Header().Set("X-BD2-Refresh-Invalid", "1")
		http.Error(w, "refresh token expired", http.StatusUnauthorized)
		return
	}
	if usedAt.Valid {
		if _, err := tx.Exec(`UPDATE families SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now.Unix(), familyID); err != nil {
			http.Error(w, "refresh unavailable", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "refresh unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-BD2-Refresh-Invalid", "1")
		http.Error(w, "refresh token replayed", http.StatusUnauthorized)
		return
	}
	newAccess, err := randomToken(32)
	if err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	newRefresh, err := randomToken(32)
	if err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	updated, err := tx.Exec(`UPDATE refresh_tokens SET used_at=? WHERE token_hash=? AND used_at IS NULL AND revoked_at IS NULL`, now.Unix(), requestTokenHash)
	if err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	if count, err := rowsAffected(updated); err != nil || count != 1 {
		w.Header().Set("X-BD2-Refresh-Invalid", "1")
		http.Error(w, "refresh token invalid", http.StatusUnauthorized)
		return
	}
	if _, err = tx.Exec(`DELETE FROM access_tokens WHERE family_id=?`, familyID); err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	if _, err = tx.Exec(`INSERT INTO access_tokens(token_hash,family_id,account_id,created_at,expires_at) VALUES(?,?,?,?,?)`, s.store.digest("access-token", newAccess), familyID, accountID, now.Unix(), now.Add(s.config.AccessTTL).Unix()); err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	refreshExpiry := min(familyExpires, now.Add(s.config.RefreshTTL).Unix())
	if _, err = tx.Exec(`INSERT INTO refresh_tokens(token_hash,family_id,created_at,expires_at) VALUES(?,?,?,?)`, s.store.digest("refresh-token", newRefresh), familyID, now.Unix(), refreshExpiry); err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	result := refreshAttemptResult{
		Provider: provider, AccessToken: newAccess, AccessExpiresAt: now.Add(s.config.AccessTTL).Unix(),
		RefreshToken: newRefresh, RefreshExpiresAt: refreshExpiry,
	}
	plain, err := json.Marshal(result)
	if err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	resultCipher, err = s.store.seal(refreshAttemptSealID(familyID, attemptHash), "result", plain)
	clear(plain)
	if err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	if _, err = tx.Exec(`INSERT INTO refresh_attempts(family_id,attempt_hash,request_token_hash,result_cipher,created_at,expires_at) VALUES(?,?,?,?,?,?)`, familyID, attemptHash, requestTokenHash, resultCipher, now.Unix(), refreshExpiry); err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "refresh unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result.deviceResult(now.Unix()))
}

func validRefreshAttemptID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, item := range value {
		if item < 'a' || item > 'z' {
			if item < 'A' || item > 'Z' {
				if item < '0' || item > '9' {
					if item != '-' && item != '_' {
						return false
					}
				}
			}
		}
	}
	return true
}

func refreshAttemptSealID(familyID string, attemptHash []byte) string {
	return familyID + ":" + base64.RawURLEncoding.EncodeToString(attemptHash)
}

func (r refreshAttemptResult) deviceResult(now int64) deviceResult {
	accessTTL := max(r.AccessExpiresAt-now, 0)
	refreshTTL := max(r.RefreshExpiresAt-now, 0)
	return deviceResult{
		Provider: r.Provider, AccessToken: r.AccessToken, AccessExpiresIn: accessTTL,
		RefreshToken: r.RefreshToken, RefreshExpiresIn: refreshTTL,
	}
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		http.Error(w, "access token required", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(authorization, "Bearer ")
	if token == "" {
		http.Error(w, "access token required", http.StatusUnauthorized)
		return
	}
	now := s.store.now().Unix()
	tx, err := s.store.db.Begin()
	if err != nil {
		http.Error(w, "revocation unavailable", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var familyID, accountStatus string
	var expires int64
	var revoked sql.NullInt64
	err = tx.QueryRow(`SELECT t.family_id,a.status,t.expires_at,COALESCE(t.revoked_at,f.revoked_at)
		FROM access_tokens t JOIN families f ON f.id=t.family_id JOIN accounts a ON a.id=t.account_id
		WHERE t.token_hash=?`, s.store.digest("access-token", token)).Scan(&familyID, &accountStatus, &expires, &revoked)
	if err != nil || accountStatus != "active" || revoked.Valid || now >= expires {
		http.Error(w, "access token invalid", http.StatusUnauthorized)
		return
	}
	result, err := tx.Exec(`UPDATE families SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now, familyID)
	if err != nil {
		http.Error(w, "revocation unavailable", http.StatusInternalServerError)
		return
	}
	if count, err := rowsAffected(result); err != nil || count != 1 {
		http.Error(w, "access token invalid", http.StatusUnauthorized)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "revocation unavailable", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) ValidateAccess(token string) (string, error) {
	if token == "" {
		return "", ErrUnauthorized
	}
	var accountID, status string
	var expires int64
	var revoked sql.NullInt64
	err := s.store.db.QueryRow(`SELECT t.account_id,a.status,t.expires_at,COALESCE(t.revoked_at,f.revoked_at) FROM access_tokens t JOIN families f ON f.id=t.family_id JOIN accounts a ON a.id=t.account_id WHERE t.token_hash=?`, s.store.digest("access-token", token)).Scan(&accountID, &status, &expires, &revoked)
	if err != nil || status != "active" || revoked.Valid || s.store.now().Unix() >= expires {
		return "", ErrUnauthorized
	}
	return accountID, nil
}

// AuthenticateLogin validates LoginUserRequest.access_token (field 2) before
// the game session is established.
func (s *Service) AuthenticateLogin(request []byte) (string, error) {
	token, found, err := wire.Bytes(request, 2)
	if err != nil || !found {
		return "", ErrUnauthorized
	}
	return s.ValidateAccess(string(token))
}

func (s *Service) redirectURL(provider string) string {
	return s.config.PublicURL + "/auth/" + provider + "/callback"
}
func providerScope(provider string) string {
	if provider == "discord" {
		return "identify"
	}
	return "openid"
}
func providerAuthorizeURL(provider string) string {
	if provider == "discord" {
		return "https://discord.com/oauth2/authorize"
	}
	return "https://accounts.google.com/o/oauth2/v2/auth"
}
func providerTokenURL(provider string) string {
	if provider == "discord" {
		return "https://discord.com/api/v10/oauth2/token"
	}
	return "https://oauth2.googleapis.com/token"
}
func providerUserURL(provider string) string {
	return "https://discord.com/api/v10/users/@me"
}
func rowsAffected(result sql.Result) (int64, error) {
	if result == nil {
		return 0, errors.New("auth: missing SQL result")
	}
	value, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("auth: count affected rows: %w", err)
	}
	return value, nil
}
