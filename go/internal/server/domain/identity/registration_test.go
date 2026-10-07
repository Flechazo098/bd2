package identity_test

import (
	"bd2server/internal/server/domain/identity"
	identitystore "bd2server/internal/server/storage/identity"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Registration authority must distinguish players even when clients use the
// same device flow. Admin authority belongs to the retained first identity.
func TestPlayerIdentityAndAdministrativeAuthorityRemainSeparate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	open := func() *identitystore.Store {
		t.Helper()
		key := make([]byte, 32)
		for i := range key {
			key[i] = byte(i + 1)
		}
		s, e := identitystore.Open(path, key)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	store := open()
	service, e := identity.New(identity.Config{Providers: map[string]string{"discord": "client"}, DeviceTTL: time.Minute, AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour}, store)
	if e != nil {
		t.Fatal(e)
	}
	login := func(subject string) identity.TokenSet {
		t.Helper()
		device, e := service.CreateDevice("discord", "127.0.0.1")
		if e != nil {
			t.Fatal(e)
		}
		auth, e := service.Start("discord", device.ID, device.StartTicket)
		if e != nil {
			t.Fatal(e)
		}
		if e = service.CompleteDevice(auth.ID, "discord", identity.ProviderIdentity{Issuer: "https://discord.com", Subject: subject}); e != nil {
			t.Fatal(e)
		}
		result, e := service.Poll(device.ID, device.Secret)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = service.Poll(device.ID, device.Secret); e == nil {
			t.Fatal("device credentials delivered twice")
		}
		return result.Tokens
	}
	a, b := login("111"), login("222")
	aID, e := service.ValidateAccess(a.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	bID, e := service.ValidateAccess(b.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if aID == bID || aID == "" || bID == "" {
		t.Fatal("distinct provider subjects share one asset owner")
	}
	profileA, e := store.GameIdentity(context.Background(), aID)
	if e != nil {
		t.Fatal(e)
	}
	profileB, e := store.GameIdentity(context.Background(), bID)
	if e != nil {
		t.Fatal(e)
	}
	if profileA.OwnerIndex <= 0 || profileB.OwnerIndex <= 0 || profileA.OwnerIndex == profileB.OwnerIndex || profileA.UserID == profileB.UserID {
		t.Fatal("different asset owners share client-visible identity")
	}
	owner, e := store.OwnerAccountID(context.Background())
	if e != nil || owner != aID {
		t.Fatalf("original owner not retained: %s %v", owner, e)
	}
	if _, e = service.AuthorizeAdministrator(a.AccessToken); e != nil {
		t.Fatal("original owner lost authority", e)
	}
	if _, e = service.AuthorizeAdministrator(b.AccessToken); !errors.Is(e, identity.ErrUnauthorized) {
		t.Fatal("new player gained administrative authority", e)
	}
	refreshed, e := service.Refresh(a.RefreshToken, "attempt-owner-00001")
	if e != nil {
		t.Fatal(e)
	}
	retried, e := service.Refresh(a.RefreshToken, "attempt-owner-00001")
	if e != nil || refreshed.AccessToken != retried.AccessToken || refreshed.RefreshToken != retried.RefreshToken {
		t.Fatal("network retry issued different credentials")
	}
	if _, e = service.Refresh(a.RefreshToken, "attempt-owner-00002"); e == nil {
		t.Fatal("used refresh token accepted as new operation")
	}
	if _, e = service.ValidateAccess(refreshed.AccessToken); e == nil {
		t.Fatal("replayed owner's family remained valid")
	}
	if actual, e := service.ValidateAccess(b.AccessToken); e != nil || actual != bID {
		t.Fatal("owner replay revoked unrelated player", e)
	}
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	store = open()
	defer func() { _ = store.Close() }()
	service, e = identity.New(identity.Config{Providers: map[string]string{"discord": "client"}, DeviceTTL: time.Minute, AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour}, store)
	if e != nil {
		t.Fatal(e)
	}
	if actual, e := service.ValidateAccess(b.AccessToken); e != nil || actual != bID {
		t.Fatal("restart mixed player credentials", e)
	}
	retainedA, e := store.GameIdentity(context.Background(), aID)
	if e != nil || retainedA != profileA {
		t.Fatal("restart changed original player public identity", e)
	}
	retainedB, e := store.GameIdentity(context.Background(), bID)
	if e != nil || retainedB != profileB {
		t.Fatal("restart changed other player public identity", e)
	}
	if owner, e = store.OwnerAccountID(context.Background()); e != nil || owner != aID {
		t.Fatal("restart reassigned administrative owner")
	}
}
