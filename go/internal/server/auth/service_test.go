package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/authconfig"
)

const testNowUnix = int64(1_800_000_000)

func testService(t *testing.T) (*Service, *Store) {
	t.Helper()
	master := bytes.Repeat([]byte{0x42}, 32)
	store, err := Open(filepath.Join(t.TempDir(), "auth.db"), master)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.now = func() time.Time { return time.Unix(testNowUnix, 0) }
	public, _ := url.Parse("https://login.example.test")
	runtime := authconfig.Runtime{
		Config: authconfig.Config{
			Mode:      "oauth",
			PublicURL: "https://login.example.test",
			Providers: map[string]authconfig.ProviderConfig{
				"discord": {ClientID: "discord-client", ClientSecretEnv: "DISCORD_SECRET"},
				"google":  {ClientID: "google-client", ClientSecretEnv: "GOOGLE_SECRET"},
			},
		},
		PublicURLParsed: public,
		MasterKey:       master,
		ProviderSecrets: map[string]string{"discord": "discord-secret", "google": "google-secret"},
		AccessTTL:       15 * time.Minute,
		RefreshTTL:      30 * 24 * time.Hour,
		DeviceTTL:       10 * time.Minute,
	}
	service, err := New(runtime, store)
	if err != nil {
		t.Fatal(err)
	}
	if service.config.MasterKey != nil {
		t.Fatal("service retained the authentication master key")
	}
	for i, value := range master {
		if value != 0 {
			t.Fatalf("master key byte %d was not cleared", i)
		}
	}
	return service, store
}

func insertAuthorizingDevice(t *testing.T, store *Store, id, provider string) {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO devices(id,client_hash,secret_hash,start_hash,provider,status,created_at,expires_at)
		VALUES(?,?,?,?,?,'authorizing',?,?)`, id, store.digest("client-ip", "192.0.2.1"), store.digest("device-secret", "device-secret"), store.digest("start-ticket", "start-ticket"), provider, testNowUnix, testNowUnix+600)
	if err != nil {
		t.Fatal(err)
	}
}

func completeAndPoll(t *testing.T, service *Service, store *Store, deviceID, provider, issuer, subject string) deviceResult {
	t.Helper()
	insertAuthorizingDevice(t, store, deviceID, provider)
	if err := service.completeDevice(deviceID, provider, providerIdentity{issuer: issuer, subject: subject}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/auth/device/"+deviceID+"/poll", nil)
	request.Header.Set("Authorization", "Device device-secret")
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%q", response.Code, response.Body.String())
	}
	var result deviceResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func postJSON(handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(value)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestCompleteDeviceRequiresAuthorizingTransition(t *testing.T) {
	service, store := testService(t)
	insertAuthorizingDevice(t, store, "already-consumed", "discord")
	if _, err := store.db.Exec(`UPDATE devices SET status='consumed' WHERE id='already-consumed'`); err != nil {
		t.Fatal(err)
	}
	err := service.completeDevice("already-consumed", "discord", providerIdentity{issuer: "https://discord.com", subject: "123456789"})
	if !errors.Is(err, ErrConsumed) {
		t.Fatalf("completeDevice error=%v, want ErrConsumed", err)
	}
	for _, table := range []string{"accounts", "identities", "families", "access_tokens", "refresh_tokens"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s has %d rows after rejected completion", table, count)
		}
	}
}

func TestPollConsumesEncryptedResultExactlyOnce(t *testing.T) {
	service, store := testService(t)
	result := completeAndPoll(t, service, store, "poll-once", "discord", "https://discord.com", "123456789")
	if result.AccessToken == "" || result.RefreshToken == "" {
		t.Fatal("poll omitted issued tokens")
	}
	request := httptest.NewRequest(http.MethodPost, "/auth/device/poll-once/poll", nil)
	request.Header.Set("Authorization", "Device device-secret")
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusGone {
		t.Fatalf("second poll status=%d body=%q", response.Code, response.Body.String())
	}
	var status string
	var cipher []byte
	if err := store.db.QueryRow(`SELECT status,COALESCE(result_cipher,X'') FROM devices WHERE id='poll-once'`).Scan(&status, &cipher); err != nil {
		t.Fatal(err)
	}
	if status != "consumed" || len(cipher) != 0 {
		t.Fatalf("device status=%q result bytes=%d", status, len(cipher))
	}
}

func TestRefreshRotationReplayRevokesFamily(t *testing.T) {
	service, store := testService(t)
	first := completeAndPoll(t, service, store, "refresh-device", "discord", "https://discord.com", "123456789")
	handler := service.Handler()
	response := postJSON(handler, "/auth/session/refresh", map[string]string{"refresh_token": first.RefreshToken})
	if response.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%q", response.Code, response.Body.String())
	}
	var rotated deviceResult
	if err := json.Unmarshal(response.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == "" || rotated.RefreshToken == first.RefreshToken || rotated.AccessToken == first.AccessToken {
		t.Fatal("refresh did not rotate both credentials")
	}
	if _, err := service.ValidateAccess(first.AccessToken); err == nil {
		t.Fatal("old access token survived refresh rotation")
	}
	if _, err := service.ValidateAccess(rotated.AccessToken); err != nil {
		t.Fatalf("new access token rejected: %v", err)
	}

	replay := postJSON(handler, "/auth/session/refresh", map[string]string{"refresh_token": first.RefreshToken})
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status=%d body=%q", replay.Code, replay.Body.String())
	}
	if _, err := service.ValidateAccess(rotated.AccessToken); err == nil {
		t.Fatal("refresh replay did not revoke the token family")
	}
	next := postJSON(handler, "/auth/session/refresh", map[string]string{"refresh_token": rotated.RefreshToken})
	if next.Code != http.StatusUnauthorized {
		t.Fatalf("family refresh after replay status=%d", next.Code)
	}
}

func TestRevokeInvalidatesAccessAndRefreshFamily(t *testing.T) {
	service, store := testService(t)
	tokens := completeAndPoll(t, service, store, "revoke-device", "discord", "https://discord.com", "123456789")
	request := httptest.NewRequest(http.MethodPost, "/auth/session/revoke", nil)
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%q", response.Code, response.Body.String())
	}
	if _, err := service.ValidateAccess(tokens.AccessToken); err == nil {
		t.Fatal("revoked access token remained valid")
	}
	refresh := postJSON(service.Handler(), "/auth/session/refresh", map[string]string{"refresh_token": tokens.RefreshToken})
	if refresh.Code != http.StatusUnauthorized {
		t.Fatalf("revoked refresh status=%d", refresh.Code)
	}
}

func TestSensitiveAuthenticationMaterialIsNotStoredInPlaintext(t *testing.T) {
	service, store := testService(t)
	handler := service.Handler()
	created := postJSON(handler, "/auth/device", map[string]string{"provider": "discord"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%q", created.Code, created.Body.String())
	}
	var device struct {
		ID       string `json:"transaction_id"`
		Secret   string `json:"device_secret"`
		StartURL string `json:"start_url"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &device); err != nil {
		t.Fatal(err)
	}
	startURL, _ := url.Parse(device.StartURL)
	ticket := startURL.Query().Get("ticket")
	start := httptest.NewRequest(http.MethodGet, startURL.RequestURI(), nil)
	started := httptest.NewRecorder()
	handler.ServeHTTP(started, start)
	if started.Code != http.StatusFound {
		t.Fatalf("start status=%d body=%q", started.Code, started.Body.String())
	}
	authorize, _ := url.Parse(started.Header().Get("Location"))
	state := authorize.Query().Get("state")
	var secretHash, startHash, stateHash, verifierCipher, nonceCipher []byte
	if err := store.db.QueryRow(`SELECT secret_hash,start_hash,state_hash,verifier_cipher,nonce_cipher FROM devices WHERE id=?`, device.ID).
		Scan(&secretHash, &startHash, &stateHash, &verifierCipher, &nonceCipher); err != nil {
		t.Fatal(err)
	}
	verifier, err := store.open(device.ID, "pkce", verifierCipher)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := store.open(device.ID, "nonce", nonceCipher)
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string]struct{ stored, raw []byte }{
		"device secret": {secretHash, []byte(device.Secret)},
		"start ticket":  {startHash, []byte(ticket)},
		"oauth state":   {stateHash, []byte(state)},
		"pkce verifier": {verifierCipher, verifier},
		"oidc nonce":    {nonceCipher, nonce},
	} {
		if bytes.Equal(pair.stored, pair.raw) || bytes.Contains(pair.stored, pair.raw) {
			t.Fatalf("%s was stored in plaintext", name)
		}
	}

	const providerSubject = "987654321012345678"
	if err := service.completeDevice(device.ID, "discord", providerIdentity{issuer: "https://discord.com", subject: providerSubject}); err != nil {
		t.Fatal(err)
	}
	var subjectHash, sealedResult []byte
	if err := store.db.QueryRow(`SELECT subject_hash FROM identities`).Scan(&subjectHash); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT result_cipher FROM devices WHERE id=?`, device.ID).Scan(&sealedResult); err != nil {
		t.Fatal(err)
	}
	plainResult, err := store.open(device.ID, "result", sealedResult)
	if err != nil {
		t.Fatal(err)
	}
	var issued deviceResult
	if err := json.Unmarshal(plainResult, &issued); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(subjectHash, []byte(providerSubject)) {
		t.Fatal("provider subject was stored in plaintext")
	}
	for name, raw := range map[string]string{"access token": issued.AccessToken, "refresh token": issued.RefreshToken} {
		if bytes.Contains(sealedResult, []byte(raw)) {
			t.Fatalf("pending %s was stored outside AES-GCM ciphertext", name)
		}
		var count int
		table := "access_tokens"
		if name == "refresh token" {
			table = "refresh_tokens"
		}
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE token_hash=?`, []byte(raw)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s was stored in plaintext", name)
		}
	}
}

func TestJSONLimitsAndSecurityHeaders(t *testing.T) {
	service, _ := testService(t)
	handler := service.Handler()
	for name, body := range map[string]struct {
		body string
		want int
	}{
		"trailing": {`{"provider":"discord"}{}`, http.StatusBadRequest},
		"oversize": {`{"provider":"discord","padding":"` + strings.Repeat("x", 17<<10) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/auth/device", strings.NewReader(body.body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != body.want {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			for header, want := range map[string]string{
				"Cache-Control":          "no-store",
				"Referrer-Policy":        "no-referrer",
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
			} {
				if got := response.Header().Get(header); got != want {
					t.Fatalf("%s=%q want %q", header, got, want)
				}
			}
		})
	}
}

func TestCreateDeviceLimitsPendingTransactionsPerClient(t *testing.T) {
	service, store := testService(t)
	handler := service.Handler()
	for i := 0; i < 5; i++ {
		response := postJSON(handler, "/auth/device", map[string]string{"provider": "discord"})
		if response.Code != http.StatusCreated {
			t.Fatalf("create %d status=%d body=%q", i, response.Code, response.Body.String())
		}
	}
	response := postJSON(handler, "/auth/device", map[string]string{"provider": "discord"})
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("pending limit status=%d body=%q", response.Code, response.Body.String())
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("device count=%d want 5", count)
	}
}

func TestDecodeProviderJSONRejectsOversizeAndTrailingValues(t *testing.T) {
	var target map[string]any
	if err := decodeProviderJSON(strings.NewReader(`{"id":"1"}{}`), &target); err == nil {
		t.Fatal("accepted provider response with trailing JSON")
	}
	oversize := `{"padding":"` + strings.Repeat("x", 1<<20) + `"}`
	if err := decodeProviderJSON(strings.NewReader(oversize), &target); err == nil {
		t.Fatal("accepted oversized provider response")
	}
}

func TestRequestLimiterIsBoundedAndExpiresWindows(t *testing.T) {
	limiter := requestLimiter{windows: make(map[string]limitWindow)}
	now := time.Unix(testNowUnix, 0)
	for i := 0; i < 4096; i++ {
		if !limiter.allow(strconv.Itoa(i), now, time.Minute, 1) {
			t.Fatalf("rejected window %d before capacity", i)
		}
	}
	if limiter.allow("overflow", now, time.Minute, 1) {
		t.Fatal("accepted a limiter key beyond its bounded capacity")
	}
	if !limiter.allow("after-expiry", now.Add(time.Minute), time.Minute, 1) {
		t.Fatal("did not clean expired limiter windows")
	}
}

func TestCleanupRetainsUsedRefreshForReplayUntilFamilyExpiry(t *testing.T) {
	service, store := testService(t)
	tokens := completeAndPoll(t, service, store, "cleanup-device", "discord", "https://discord.com", "123456789")
	if _, err := store.db.Exec(`UPDATE refresh_tokens SET used_at=? WHERE token_hash=?`, testNowUnix, store.digest("refresh-token", tokens.RefreshToken)); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupExpired(tx, testNowUnix+int64((29*24*time.Hour).Seconds())); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE used_at IS NOT NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("used refresh token was removed before family expiry")
	}
	tx, err = store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupExpired(tx, testNowUnix+int64((31*24*time.Hour).Seconds())); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("expired family refresh token was not cleaned")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestProviderIdentityVerification(t *testing.T) {
	service, _ := testService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host + request.URL.Path {
		case "discord.com/api/v10/oauth2/token":
			return jsonResponse(http.StatusOK, `{"access_token":"provider-access"}`), nil
		case "discord.com/api/v10/users/@me":
			if request.Header.Get("Authorization") != "Bearer provider-access" {
				t.Fatal("Discord bearer token missing")
			}
			return jsonResponse(http.StatusOK, `{"id":"123456789"}`), nil
		case "oauth2.googleapis.com/token":
			return jsonResponse(http.StatusOK, `{"access_token":"provider-access","id_token":"signed-id-token"}`), nil
		case "oauth2.googleapis.com/tokeninfo":
			return jsonResponse(http.StatusOK, `{"iss":"https://accounts.google.com","aud":"google-client","sub":"google-subject","nonce":"expected-nonce","exp":"1900000000"}`), nil
		default:
			t.Fatalf("unexpected provider request %s", request.URL)
			return nil, nil
		}
	})}
	discord, err := service.exchangeIdentity(context.Background(), "discord", "code", "verifier", "nonce")
	if err != nil || discord.issuer != "https://discord.com" || discord.subject != "123456789" {
		t.Fatalf("Discord identity=%+v err=%v", discord, err)
	}
	google, err := service.exchangeIdentity(context.Background(), "google", "code", "verifier", "expected-nonce")
	if err != nil || google.issuer != "https://accounts.google.com" || google.subject != "google-subject" {
		t.Fatalf("Google identity=%+v err=%v", google, err)
	}
	if _, err := service.exchangeIdentity(context.Background(), "google", "code", "verifier", "wrong-nonce"); err == nil {
		t.Fatal("Google identity accepted the wrong OIDC nonce")
	}
}

func TestProviderFailureIsStructuredAndSanitized(t *testing.T) {
	service, _ := testService(t)
	secretDescription := "provider leaked secret sentinel"
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":"invalid_client","error_description":"`+secretDescription+`"}`), nil
	})}
	_, err := service.exchangeIdentity(context.Background(), "discord", "code", "verifier", "nonce")
	var failure *providerFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error %T does not expose provider failure", err)
	}
	if failure.Provider != "discord" || failure.Stage != "token_exchange" || failure.Reason != "http_rejected" ||
		failure.HTTPStatus != http.StatusUnauthorized || failure.OAuthError != "invalid_client" {
		t.Fatalf("failure=%+v", failure)
	}
	if strings.Contains(err.Error(), secretDescription) {
		t.Fatal("provider error description leaked through diagnostic error")
	}
}

func TestProviderFailureRejectsUntrustedOAuthError(t *testing.T) {
	service, _ := testService(t)
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusBadRequest, `{"error":"access-token-sentinel"}`), nil
	})}
	_, err := service.exchangeIdentity(context.Background(), "discord", "code", "verifier", "nonce")
	var failure *providerFailure
	if !errors.As(err, &failure) || failure.OAuthError != "unknown" {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}
	if strings.Contains(err.Error(), "access-token-sentinel") {
		t.Fatal("untrusted provider error leaked through diagnostic error")
	}
}

func TestGoogleProviderNetworkFailureDoesNotLeakIDTokenURL(t *testing.T) {
	service, _ := testService(t)
	idToken := "signed-id-token-sentinel"
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/token" {
			return jsonResponse(http.StatusOK, `{"access_token":"provider-access","id_token":"`+idToken+`"}`), nil
		}
		return nil, &url.Error{Op: "Get", URL: "https://oauth2.googleapis.com/tokeninfo?id_token=" + idToken, Err: errors.New("transport sentinel")}
	})}
	_, err := service.exchangeIdentity(context.Background(), "google", "code", "verifier", "nonce")
	var failure *providerFailure
	if !errors.As(err, &failure) || failure.Provider != "google" || failure.Stage != "id_token_verify" || failure.Reason != "network_error" {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}
	if strings.Contains(err.Error(), idToken) || strings.Contains(err.Error(), "transport sentinel") {
		t.Fatal("Google ID token URL or transport details leaked through diagnostic error")
	}
}

func TestCallbackFailureClearsShortLivedOAuthMaterial(t *testing.T) {
	service, store := testService(t)
	insertAuthorizingDevice(t, store, "failed-device", "discord")
	state := "failed-state"
	verifier, err := store.seal("failed-device", "pkce", []byte("verifier"))
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := store.seal("failed-device", "nonce", []byte("nonce"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE devices SET state_hash=?,verifier_cipher=?,nonce_cipher=? WHERE id='failed-device'`, store.digest("oauth-state", state), verifier, nonce); err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":"invalid_client"}`), nil
	})}
	request := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?code=failed-code&state="+url.QueryEscape(state), nil)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "server OAuth configuration is invalid") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	var status string
	var stateHash, verifierCipher, nonceCipher []byte
	if err := store.db.QueryRow(`SELECT status,COALESCE(state_hash,X''),COALESCE(verifier_cipher,X''),COALESCE(nonce_cipher,X'') FROM devices WHERE id='failed-device'`).Scan(&status, &stateHash, &verifierCipher, &nonceCipher); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || len(stateHash) != 0 || len(verifierCipher) != 0 || len(nonceCipher) != 0 {
		t.Fatalf("status=%q state=%d verifier=%d nonce=%d", status, len(stateHash), len(verifierCipher), len(nonceCipher))
	}
}

func TestProviderScopesUseLeastPrivilege(t *testing.T) {
	if got := providerScope("discord"); got != "identify" {
		t.Fatalf("Discord scope=%q, want identify", got)
	}
	if got := providerScope("google"); got != "openid" {
		t.Fatalf("Google scope=%q, want openid", got)
	}
}

func TestProviderErrorConsumesAuthorizationStateWithoutExchange(t *testing.T) {
	service, store := testService(t)
	insertAuthorizingDevice(t, store, "cancelled-device", "discord")
	state := "cancelled-oauth-state"
	if _, err := store.db.Exec(`UPDATE devices SET state_hash=? WHERE id='cancelled-device'`, store.digest("oauth-state", state)); err != nil {
		t.Fatal(err)
	}
	service.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatal("provider error callback attempted a token exchange")
		return nil, nil
	})}
	request := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?error=access_denied&state="+url.QueryEscape(state), nil)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	var status, errorCode string
	var stateHash, verifier, nonce []byte
	if err := store.db.QueryRow(`SELECT status,error_code,COALESCE(state_hash,X''),COALESCE(verifier_cipher,X''),COALESCE(nonce_cipher,X'') FROM devices WHERE id='cancelled-device'`).
		Scan(&status, &errorCode, &stateHash, &verifier, &nonce); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errorCode != "provider_cancelled" || len(stateHash) != 0 || len(verifier) != 0 || len(nonce) != 0 {
		t.Fatalf("cancelled device status=%q error=%q state=%d verifier=%d nonce=%d", status, errorCode, len(stateHash), len(verifier), len(nonce))
	}
}

func TestCompleteDeviceRejectsMalformedProviderIdentity(t *testing.T) {
	service, store := testService(t)
	insertAuthorizingDevice(t, store, "bad-identity", "discord")
	if err := service.completeDevice("bad-identity", "discord", providerIdentity{issuer: "https://discord.com", subject: "not-a-snowflake"}); err == nil {
		t.Fatal("accepted malformed Discord identity")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM identities`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("malformed identity was persisted")
	}
}
