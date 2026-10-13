package auth

import (
	"bd2server/internal/server/domain/identity"
	"bd2server/internal/server/gateway/authconfig"
	identitystore "bd2server/internal/server/storage/identity"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSessionProfileUsesAuthenticatedAccount(t *testing.T) {
	identityPath := filepath.Join(t.TempDir(), "identity.db")
	store, err := identitystore.Open(identityPath, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	service, err := New(authconfig.Runtime{
		Config:    authconfig.Config{Mode: "oauth", Providers: map[string]authconfig.ProviderConfig{"discord": {ClientID: "test-client"}}},
		AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour, DeviceTTL: time.Minute,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	login := func(subject string) identity.TokenSet {
		t.Helper()
		device, err := service.identity.CreateDevice("discord", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		authorization, err := service.identity.Start("discord", device.ID, device.StartTicket)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.identity.CompleteDevice(authorization.ID, "discord", identity.ProviderIdentity{Issuer: "https://discord.com", Subject: subject}); err != nil {
			t.Fatal(err)
		}
		result, err := service.identity.Poll(device.ID, device.Secret)
		if err != nil {
			t.Fatal(err)
		}
		return result.Tokens
	}
	a, b := login("111111"), login("222222")
	profileFor := func(token string) identity.GameProfile {
		t.Helper()
		account, err := service.ValidateAccess(token)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := store.GameIdentity(context.Background(), account)
		if err != nil {
			t.Fatal(err)
		}
		return identity.GameProfile{AccountID: account, UID: strconv.FormatInt(profile.OwnerIndex, 10)}
	}
	aProfile, bProfile := profileFor(a.AccessToken), profileFor(b.AccessToken)
	if aProfile.AccountID == bProfile.AccountID || aProfile.UID == bProfile.UID {
		t.Fatal("device registrations did not create distinct game identities")
	}
	// Imported profiles can retain a display name in user_id; the client UID
	// shown by UserInfoPanel.UpdateUID is the numeric owner_index.
	db, err := sql.Open("sqlite", identityPath)
	if err != nil {
		t.Fatal(err)
	}
	nickname := "Guest_" + aProfile.UID
	if _, err := db.Exec(`UPDATE game_profiles SET user_id=? WHERE account_id=?`, nickname, aProfile.AccountID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	retained, err := store.GameIdentity(context.Background(), aProfile.AccountID)
	if err != nil || retained.UserID != nickname || strconv.FormatInt(retained.OwnerIndex, 10) != aProfile.UID {
		t.Fatalf("persisted nickname and UID fixture = %+v: %v", retained, err)
	}
	handler := service.Handler()
	request := func(method, path, authorization string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, authorization := range []string{"", "Bearer ", "Device secret", "Bearer invalid"} {
		if w := request(http.MethodGet, "/auth/session/profile", authorization); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized profile status = %d", w.Code)
		}
	}
	assertProfile := func(path, token string, want identity.GameProfile) {
		t.Helper()
		w := request(http.MethodGet, path, "Bearer "+token)
		if w.Code != http.StatusOK {
			t.Fatalf("profile status = %d: %s", w.Code, w.Body.String())
		}
		var fields map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 2 || fields["account_id"] != want.AccountID || fields["uid"] != want.UID {
			t.Fatalf("profile = %v, want account %s UID %s", fields, want.AccountID, want.UID)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("profile response can be cached")
		}
	}
	assertProfile("/auth/session/profile", a.AccessToken, aProfile)
	assertProfile("/auth/session/profile", b.AccessToken, bProfile)
	query := url.Values{"account_id": {bProfile.AccountID}, "uid": {bProfile.UID}}
	assertProfile("/auth/session/profile?"+query.Encode(), a.AccessToken, aProfile)
	if w := request(http.MethodPost, "/auth/session/revoke", "Bearer "+a.AccessToken); w.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d: %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/auth/session/profile", "Bearer "+a.AccessToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked profile status = %d", w.Code)
	}
	if _, err := service.identity.Refresh(a.RefreshToken, "binding-revoked-family"); err == nil {
		t.Fatal("binding token revocation left its refresh token usable")
	}
	assertProfile("/auth/session/profile", b.AccessToken, bProfile)
}
