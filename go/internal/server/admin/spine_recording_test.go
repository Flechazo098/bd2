package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"bd2server/internal/server/design/gameconfig"
)

func TestOwnerCanConfigureRecordingProxyForNextServerStart(t *testing.T) {
	console, identity, _ := newConsole(t)
	server := httptest.NewServer(console.Handler())
	defer server.Close()
	owner := newBrowser(t, server)
	if owner.login(identity, "owner") != http.StatusOK {
		t.Fatal("owner could not open the configuration editor")
	}
	status, document := owner.do(http.MethodGet, "/admin/api/config/game", nil, nil)
	if status != http.StatusOK {
		t.Fatal("game settings are unavailable")
	}
	view := document["view"].(map[string]any)
	value := view["value"].(map[string]any)
	value["spine_recording"] = map[string]string{"download_base_url": "https://replay.example.com/recordings"}
	status, saved := owner.do(http.MethodPut, "/admin/api/config/game", value, map[string]string{"If-Match": view["revision"].(string)})
	if status != http.StatusOK || saved["pending"] != true {
		t.Fatalf("recording URL was not saved for restart: status=%d", status)
	}
	restarted, err := gameconfig.Document.Load(saved["path"].(string))
	if err != nil || restarted.SpineRecording.DownloadBaseURL != "https://replay.example.com/recordings" {
		t.Fatalf("the next server start would lose the owner's recording proxy: %v", err)
	}
	for _, invalid := range []string{"javascript:alert(1)", "https://user:password@example.com/replays", "https://example.com/replays?token=secret", "http://example.com/replays"} {
		value["spine_recording"] = map[string]string{"download_base_url": invalid}
		status, _ = owner.do(http.MethodPut, "/admin/api/config/game", value, map[string]string{"If-Match": saved["revision"].(string)})
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("unsafe recording address replaced a working proxy: status=%d", status)
		}
	}
	restarted, err = gameconfig.Document.Load(saved["path"].(string))
	if err != nil || restarted.SpineRecording.DownloadBaseURL != "https://replay.example.com/recordings" {
		t.Fatalf("rejected edit damaged the working recording URL: %v", err)
	}
	value["spine_recording"] = map[string]string{"download_base_url": ""}
	status, saved = owner.do(http.MethodPut, "/admin/api/config/game", value, map[string]string{"If-Match": saved["revision"].(string)})
	if status != http.StatusOK {
		t.Fatal("owner could not restore this server as the recording source")
	}
	restarted, err = gameconfig.Document.Load(saved["path"].(string))
	if err != nil || restarted.SpineRecording.DownloadBaseURL != "" {
		t.Fatalf("clearing the proxy did not persist: %v", err)
	}
}
