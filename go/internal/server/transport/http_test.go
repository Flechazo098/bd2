package transport

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/authconfig"
	"bd2server/internal/server/bootstrap"
	"bd2server/internal/server/resourcepolicy"
	"bd2server/internal/server/wire"
)

func TestBootstrapRoundTrip(t *testing.T) {
	cfg := bootstrap.Config{BaseURL: "http://127.0.0.1:8080/game/", CDNURL: "http://127.0.0.1:8080/assets/ServerData", Version: "test-client", BundleVer: "test-bundle"}
	now := func() time.Time { return time.UnixMilli(12345) }
	h := HTTP{Dispatcher: Bootstrap{Config: cfg, Now: now}, Now: now}.Handler()
	for _, path := range []string{"/MaintenanceInfo", "/ServerInfo", "/NoticeInfo", "/ServerNowTime"} {
		request := base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 2))
		req := httptest.NewRequest(http.MethodPut, "/game"+path, strings.NewReader(request))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
		}
		var envelope Envelope
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil || envelope.ErrorType != 0 || envelope.ServerNowTime != 12345 {
			t.Fatalf("%s: %v %+v", path, err, envelope)
		}
		if _, err := base64.StdEncoding.DecodeString(envelope.Data); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestAuthenticationConfig(t *testing.T) {
	h := HTTP{Authentication: authconfig.Config{
		Mode: "oauth", PublicURL: "https://example.com", MasterKeyEnv: "MASTER",
		Providers: map[string]authconfig.ProviderConfig{
			"discord": {ClientID: "d", ClientSecretEnv: "DS"},
			"google":  {ClientID: "g", ClientSecretEnv: "GS"},
		},
	}}.Handler()
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/auth/config", nil))
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%q", res.Code, res.Header().Get("Cache-Control"), res.Body.String())
	}
	var config authconfig.Public
	if err := json.Unmarshal(res.Body.Bytes(), &config); err != nil || config.Mode != "oauth" || len(config.Providers) != 2 {
		t.Fatalf("config=%+v err=%v", config, err)
	}

	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/auth/config", nil))
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", res.Code)
	}
}

type cookieRawDispatcher struct{}

func (cookieRawDispatcher) DispatchRaw(string, []byte, string) (RawReply, error) {
	return RawReply{Body: []byte(`{}`), Cookie: "0123456789abcdef0123456789abcdef0123456789abcdef|1"}, nil
}

type failedRawDispatcher struct {
	err error
}

func (d failedRawDispatcher) DispatchRaw(string, []byte, string) (RawReply, error) {
	return RawReply{}, d.err
}

func TestExpiredGameSessionUsesDedicatedHTTPMarker(t *testing.T) {
	h := HTTP{Raw: failedRawDispatcher{err: ErrGameSessionExpired}}.Handler()
	request := httptest.NewRequest(http.MethodPut, "/game/BatchRequest", strings.NewReader("encrypted"))
	request.Header.Set("Cookie", "s=0123456789abcdef0123456789abcdef0123456789abcdef|1")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || response.Header().Get("X-BD2-Session-Expired") != "1" ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d marker=%q cache=%q body=%q", response.Code,
			response.Header().Get("X-BD2-Session-Expired"), response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func TestDomainFailureDoesNotUseExpiredSessionMarker(t *testing.T) {
	h := HTTP{Raw: failedRawDispatcher{err: errors.New("mail seed is invalid")}}.Handler()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/game/MailInfo", strings.NewReader("encrypted")))
	if response.Code != http.StatusBadRequest || response.Header().Get("X-BD2-Session-Expired") != "" {
		t.Fatalf("status=%d marker=%q body=%q", response.Code,
			response.Header().Get("X-BD2-Session-Expired"), response.Body.String())
	}
}

func TestOAuthGameSessionCookieIsHostOnlySecureAndGameScoped(t *testing.T) {
	h := HTTP{
		Raw: cookieRawDispatcher{},
		Authentication: authconfig.Config{
			Mode: "oauth", PublicURL: "https://example.com", MasterKeyEnv: "MASTER",
			Providers: map[string]authconfig.ProviderConfig{
				"discord": {ClientID: "d", ClientSecretEnv: "DS"},
			},
		},
	}.Handler()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/game/LoginUser", strings.NewReader("request")))
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "s" || cookie.Path != "/game/" || !cookie.HttpOnly || !cookie.Secure ||
		cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" {
		t.Fatalf("unsafe game session cookie: %+v", cookie)
	}
}

func TestUnknownPacketFailsClosed(t *testing.T) {
	h := HTTP{Dispatcher: Bootstrap{}}.Handler()
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/game/InventedPacket", strings.NewReader("AA==")))
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("unknown packet status: %d", res.Code)
	}
}

func TestClientResourcePolicyUsesPUTAndDoesNotCache(t *testing.T) {
	policy := resourcepolicy.Public{
		Mode: resourcepolicy.ModeServer, ServerDataURL: "https://cdn.example.com/ServerData",
		GameDataURL: "https://cdn.example.com/GameData", BundleVersion: "bundle", GameDataVersion: "game-data",
	}
	h := HTTP{ResourcePolicy: policy}.Handler()
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/client/resources", strings.NewReader(`{"cdn_mode":"server"}`)))
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%q", res.Code, res.Header().Get("Cache-Control"), res.Body.String())
	}
	var got resourcepolicy.Public
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || got != policy {
		t.Fatalf("policy=%+v err=%v", got, err)
	}

	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/client/resources", nil))
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", res.Code)
	}
}

func TestClientResourcePolicyRejectsInvalidOrMismatchedSelection(t *testing.T) {
	policy := resourcepolicy.Public{
		Mode: resourcepolicy.ModeServer, ServerDataURL: "https://cdn.example.com/ServerData",
		GameDataURL: "https://cdn.example.com/GameData", BundleVersion: "bundle", GameDataVersion: "game-data",
	}
	h := HTTP{ResourcePolicy: policy}.Handler()
	for name, test := range map[string]struct {
		body   string
		status int
	}{
		"missing":              {`{}`, http.StatusBadRequest},
		"unknown":              {`{"cdn_mode":"server","url":"https://evil.example"}`, http.StatusBadRequest},
		"trailing":             {`{"cdn_mode":"server"}{}`, http.StatusBadRequest},
		"official":             {`{"cdn_mode":"official"}`, http.StatusBadRequest},
		"local":                {`{"cdn_mode":"local"}`, http.StatusBadRequest},
		"legacy-self-hosted":   {`{"cdn_mode":"self_hosted"}`, http.StatusBadRequest},
		"legacy-reverse-proxy": {`{"cdn_mode":"reverse_proxy"}`, http.StatusBadRequest},
		"too-large":            {`{"cdn_mode":"server","padding":"` + strings.Repeat("x", 16<<10) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			res := httptest.NewRecorder()
			h.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/client/resources", strings.NewReader(test.body)))
			if res.Code != test.status {
				t.Fatalf("status=%d want=%d body=%q", res.Code, test.status, res.Body.String())
			}
		})
	}
}

func TestClientResourcePolicyRejectsServerSelectionWhenServerUsesOfficialResources(t *testing.T) {
	policy := resourcepolicy.Public{
		Mode: resourcepolicy.ModeOfficial, ServerDataURL: resourcepolicy.OfficialServerDataURL,
		GameDataURL: resourcepolicy.OfficialGameDataURL, BundleVersion: "bundle", GameDataVersion: "game-data",
	}
	res := httptest.NewRecorder()
	HTTP{ResourcePolicy: policy}.Handler().ServeHTTP(
		res,
		httptest.NewRequest(http.MethodPut, "/client/resources", strings.NewReader(`{"cdn_mode":"server"}`)),
	)
	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d want=%d body=%q", res.Code, http.StatusConflict, res.Body.String())
	}
}
