package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bd2server/internal/common/release"
	"bd2server/internal/server/gateway/transport"
)

type updateAdmissionWorld struct{ writes int }

func (w *updateAdmissionWorld) DispatchRaw(context.Context, string, []byte, string) (transport.RawReply, error) {
	w.writes++
	return transport.RawReply{Body: []byte(`{"ok":true}`), ContentType: "application/json"}, nil
}

func TestServerSwitchRejectsOldSessionBeforeStateMutation(t *testing.T) {
	world := &updateAdmissionWorld{}
	active := updateCompatibility{}
	endpoint := transport.HTTP{Raw: world, ClientAllowed: func(h http.Header) bool { return active.allowed(h) }, Compatibility: func() any { return map[string]any{"fingerprint": active.summary()} }, StoryCheckpoints: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { world.writes++; w.WriteHeader(http.StatusOK) }), RealtimeTickets: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { world.writes++; w.WriteHeader(http.StatusOK) })}.Handler()
	request := func(path string, identity release.ClientIdentity) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{}`))
		r.Header.Set("Cookie", "old-session-before-maintenance")
		r.Header.Set("X-BD2-Client-Version", identity.Version)
		r.Header.Set("X-BD2-Update-Fingerprint", identity.Fingerprint)
		r.Header.Set("X-BD2-Resource-Fingerprint", identity.ResourceFingerprint)
		r.Header.Set("X-BD2-Game-Version", identity.GameVersion)
		r.Header.Set("X-BD2-Bundle-Version", identity.BundleVersion)
		r.Header.Set("X-BD2-Game-Data-Version", identity.GameDataVersion)
		plugins, err := json.Marshal(identity.Plugins)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-BD2-Plugins", string(plugins))
		w := httptest.NewRecorder()
		endpoint.ServeHTTP(w, r)
		return w
	}
	old := release.ClientIdentity{Version: "2.36.7+client.0.1.0", Plugins: map[string]string{"login_ui": "0.2.0"}, GameVersion: "2.36.7", BundleVersion: "20261007175947", GameDataVersion: "20261008155037", Fingerprint: "old-content"}
	old.ResourceFingerprint = "previous-resource-content"
	if w := request("/game/SaveUserPosition", old); w.Code != 200 || world.writes != 1 {
		t.Fatal("pre-maintenance session could not save")
	}
	active.fingerprint = &release.Fingerprint{ID: "new-content", ClientVersion: "2.36.7+client.0.2.0", RequireClientUpdate: true, RequireGameUpdate: true, MinimumPlugins: map[string]string{"login_ui": "0.3.0"}, Game: &release.GameResources{GameVersion: old.GameVersion, BundleVersion: old.BundleVersion, GameDataVersion: old.GameDataVersion}}
	active.fingerprint.GameFingerprint = "new-resource-content"
	for _, path := range []string{"/game/SaveUserPosition", "/game/BatchRequest", "/game/LoginUser", "/client/story-checkpoint", "/client/realtime-ticket"} {
		w := request(path, old)
		if w.Code != 409 || w.Header().Get("X-BD2-Update-Required") != "1" {
			t.Fatalf("old client was not told to update at %s: %d", path, w.Code)
		}
	}
	if world.writes != 1 {
		t.Fatal("old client mutated player state after the server switch")
	}
	newer := old
	newer.Version = active.fingerprint.ClientVersion
	newer.Plugins = map[string]string{"login_ui": "0.3.0"}
	if w := request("/game/LoginUser", newer); w.Code != 409 {
		t.Fatal("same-version but outdated resource content was accepted")
	}
	newer.Fingerprint = "new-content"
	newer.ResourceFingerprint = "new-resource-content"
	if w := request("/game/LoginUser", newer); w.Code != 200 || world.writes != 2 {
		t.Fatal("fully updated client could not log in")
	}
	active.fingerprint.ID = "later-server-only-release"
	if w := request("/game/SaveUserPosition", newer); w.Code != 200 {
		t.Fatal("server-only release rejected unchanged verified game content")
	}
	if w := request("/game/LoginUser", old); w.Code != 409 {
		t.Fatal("later server-only release forgot an earlier mandatory update")
	}
	w := httptest.NewRecorder()
	endpoint.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/client/compatibility", nil))
	if w.Code != 200 {
		t.Fatal("incompatible client cannot discover required update")
	}
	active.fingerprint.RequireClientUpdate = false
	active.fingerprint.RequireGameUpdate = false
	if w := request("/game/LoginUser", old); w.Code != 200 {
		t.Fatal("compatible server-only update forced a client update")
	}
}
