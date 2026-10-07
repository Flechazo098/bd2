package auth

import (
	"bd2server/internal/server/domain/identity"
	"bd2server/internal/server/gateway/authconfig"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"context"
	"crypto/sha256"
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
)

type Service struct {
	config   authconfig.Runtime
	identity *identity.Service
	client   *http.Client
	limits   requestLimiter
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

func New(config authconfig.Runtime, store identity.Repository) (*Service, error) {
	if config.Mode != "oauth" || store == nil {
		return nil, errors.New("auth: OAuth service requires oauth configuration and store")
	}
	clear(config.MasterKey)
	config.MasterKey = nil
	providers := map[string]string{}
	for name, p := range config.Providers {
		providers[name] = p.ClientID
	}
	service, err := identity.New(identity.Config{Providers: providers, AccessTTL: config.AccessTTL, RefreshTTL: config.RefreshTTL, DeviceTTL: config.DeviceTTL}, store)
	if err != nil {
		return nil, err
	}
	return &Service{config: config, identity: service, client: &http.Client{Timeout: 15 * time.Second}, limits: requestLimiter{windows: map[string]limitWindow{}}}, nil
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
	if err = decoder.Decode(target); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
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
func tokenResponse(r identity.TokenSet) map[string]any {
	return map[string]any{"provider": r.Provider, "access_token": r.AccessToken, "access_expires_in": r.AccessExpiresIn, "refresh_token": r.RefreshToken, "refresh_expires_in": r.RefreshExpiresIn}
}
func writeFailure(w http.ResponseWriter, err error, fallback string) {
	status := http.StatusInternalServerError
	message := fallback
	var failure *identity.Failure
	if errors.As(err, &failure) {
		message = failure.Message
		if failure.RefreshInvalid {
			w.Header().Set("X-BD2-Refresh-Invalid", "1")
		}
		switch failure.Kind {
		case identity.Invalid:
			status = http.StatusBadRequest
		case identity.Forbidden:
			status = http.StatusForbidden
		case identity.Expired:
			status = http.StatusGone
		case identity.Conflict:
			status = http.StatusConflict
		case identity.Unauthorized:
			status = http.StatusUnauthorized
		case identity.TooManyPending:
			status = http.StatusTooManyRequests
		}
	}
	http.Error(w, message, status)
}
func (s *Service) createDevice(w http.ResponseWriter, r *http.Request) {
	clientIP := remoteIP(r.RemoteAddr)
	if !s.limits.allow("create:"+clientIP, s.identity.Now(), time.Minute, 10) {
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
	result, err := s.identity.CreateDevice(request.Provider, clientIP)
	if err != nil {
		var failure *identity.Failure
		if errors.As(err, &failure) && failure.Kind == identity.TooManyPending {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(s.config.DeviceTTL.Seconds()), 10))
		}
		writeFailure(w, err, "could not create transaction")
		return
	}
	start := *s.config.PublicURLParsed
	start.Path = "/auth/" + request.Provider + "/start"
	query := start.Query()
	query.Set("transaction_id", result.ID)
	query.Set("ticket", result.StartTicket)
	start.RawQuery = query.Encode()
	writeJSON(w, http.StatusCreated, map[string]any{"transaction_id": result.ID, "device_secret": result.Secret, "start_url": start.String(), "expires_in": int64(s.config.DeviceTTL.Seconds()), "poll_interval": 2})
}
func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if _, ok := s.config.Providers[provider]; !ok {
		http.Error(w, "provider is not enabled", http.StatusNotFound)
		return
	}
	auth, err := s.identity.Start(provider, r.URL.Query().Get("transaction_id"), r.URL.Query().Get("ticket"))
	if err != nil {
		writeFailure(w, err, "could not start authorization")
		return
	}
	challenge := sha256.Sum256([]byte(auth.Verifier))
	values := url.Values{"client_id": {s.config.Providers[provider].ClientID}, "redirect_uri": {s.redirectURL(provider)}, "response_type": {"code"}, "scope": {providerScope(provider)}, "state": {auth.State}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	if provider == "google" {
		values.Set("nonce", auth.Nonce)
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
		if err := s.identity.CancelAuthorization(provider, state); err != nil {
			writeFailure(w, err, "authorization state unavailable")
			return
		}
		http.Error(w, "authorization was cancelled", http.StatusBadRequest)
		return
	}
	if code == "" {
		http.Error(w, "authorization was not completed", http.StatusBadRequest)
		return
	}
	auth, err := s.identity.Authorization(provider, state)
	if err != nil {
		writeFailure(w, err, "authorization state unavailable")
		return
	}
	id, err := s.exchangeIdentity(r.Context(), provider, code, auth.Verifier, auth.Nonce)
	auth.Verifier, auth.Nonce = "", ""
	if err != nil {
		s.identity.RejectProvider(auth.ID)
		var failure *providerFailure
		if errors.As(err, &failure) {
			slog.Warn("OAuth provider authorization failed", "provider", failure.Provider, "stage", failure.Stage, "reason", failure.Reason, "http_status", failure.HTTPStatus, "oauth_error", failure.OAuthError)
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
	if err = s.identity.CompleteDevice(auth.ID, provider, id); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, identity.ErrNotAllowed) {
			status = http.StatusForbidden
		} else if errors.Is(err, identity.ErrConsumed) {
			status = http.StatusConflict
		}
		http.Error(w, "authorization could not be completed", status)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>BD2 login</title><p>Login complete. You can return to the game.</p>`)
}
func (s *Service) poll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.limits.allow("poll:"+remoteIP(r.RemoteAddr)+":"+id, s.identity.Now(), time.Minute, 60) {
		w.Header().Set("Retry-After", "2")
		http.Error(w, "poll rate exceeded", http.StatusTooManyRequests)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Device ") {
		http.Error(w, "invalid device transaction", http.StatusForbidden)
		return
	}
	result, err := s.identity.Poll(id, strings.TrimPrefix(auth, "Device "))
	if err != nil {
		writeFailure(w, err, "login result unavailable")
		return
	}
	switch result.Status {
	case "pending":
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "retry_after": 2})
	case "failed":
		writeJSON(w, http.StatusForbidden, map[string]string{"status": "failed", "error": result.ErrorCode})
	case "complete":
		writeJSON(w, http.StatusOK, tokenResponse(result.Tokens))
	}
}
func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refresh_token"`
		AttemptID    string `json:"attempt_id"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	tokens, err := s.identity.Refresh(request.RefreshToken, request.AttemptID)
	if err != nil {
		writeFailure(w, err, "refresh unavailable")
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse(tokens))
}
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") == "" {
		http.Error(w, "access token required", http.StatusUnauthorized)
		return
	}
	if err := s.identity.Revoke(strings.TrimPrefix(auth, "Bearer ")); err != nil {
		writeFailure(w, err, "revocation unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Service) ValidateAccess(token string) (string, error) {
	return s.identity.ValidateAccess(token)
}
func (s *Service) AuthenticateLogin(request []byte) (string, error) {
	token, found, err := wire.Bytes(request, 2)
	if err != nil || !found {
		return "", identity.ErrUnauthorized
	}
	return s.identity.ValidateAccess(string(token))
}

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

func (s *Service) exchangeIdentity(ctx context.Context, provider, code, verifier, nonce string) (identity.ProviderIdentity, error) {
	values := url.Values{"client_id": {s.config.Providers[provider].ClientID}, "client_secret": {s.config.ProviderSecrets[provider]}, "grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.redirectURL(provider)}, "code_verifier": {verifier}}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, providerTokenURL(provider), strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		return identity.ProviderIdentity{}, networkProviderFailure(ctx, provider, "token_exchange", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return identity.ProviderIdentity{}, rejectedProviderFailure(provider, "token_exchange", response)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := decodeProviderJSON(response.Body, &token); err != nil || token.AccessToken == "" {
		return identity.ProviderIdentity{}, invalidProviderResponse(provider, "token_exchange")
	}
	if provider == "google" {
		if token.IDToken == "" {
			return identity.ProviderIdentity{}, invalidProviderResponse(provider, "token_exchange")
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
		return identity.ProviderIdentity{}, networkProviderFailure(ctx, provider, "userinfo", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return identity.ProviderIdentity{}, rejectedProviderFailure(provider, "userinfo", response)
	}
	var user struct {
		ID  string `json:"id"`
		Sub string `json:"sub"`
	}
	if err := decodeProviderJSON(response.Body, &user); err != nil {
		return identity.ProviderIdentity{}, invalidProviderResponse(provider, "userinfo")
	}
	if provider == "discord" && user.ID != "" {
		return identity.ProviderIdentity{Issuer: "https://discord.com", Subject: user.ID}, nil
	}
	return identity.ProviderIdentity{}, invalidProviderResponse(provider, "userinfo")
}

// verifyGoogleIDToken delegates signature and standard-claim verification to
// Google's HTTPS tokeninfo endpoint, then independently verifies this server's
// audience, nonce and expiry. The raw ID token is never persisted or logged.
func (s *Service) verifyGoogleIDToken(ctx context.Context, idToken, nonce string) (identity.ProviderIdentity, error) {
	endpoint := "https://oauth2.googleapis.com/tokeninfo?id_token=" + url.QueryEscape(idToken)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	response, err := s.client.Do(request)
	if err != nil {
		return identity.ProviderIdentity{}, networkProviderFailure(ctx, "google", "id_token_verify", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return identity.ProviderIdentity{}, rejectedProviderFailure("google", "id_token_verify", response)
	}
	var claims struct {
		Issuer   string `json:"iss"`
		Audience string `json:"aud"`
		Subject  string `json:"sub"`
		Nonce    string `json:"nonce"`
		Expires  string `json:"exp"`
	}
	if err := decodeProviderJSON(response.Body, &claims); err != nil {
		return identity.ProviderIdentity{}, invalidProviderResponse("google", "id_token_verify")
	}
	expires, err := strconv.ParseInt(claims.Expires, 10, 64)

	if err != nil || !s.identity.ValidateGoogleClaims(claims.Issuer, claims.Audience, claims.Subject, claims.Nonce, nonce, expires) {
		return identity.ProviderIdentity{}, &providerFailure{Provider: "google", Stage: "id_token_verify", Reason: "invalid_claims"}
	}
	return identity.ProviderIdentity{Issuer: "https://accounts.google.com", Subject: claims.Subject}, nil
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

func (s *Service) AuthorizeAdministrator(token string) (string, error) {
	return s.identity.AuthorizeAdministrator(token)
}
