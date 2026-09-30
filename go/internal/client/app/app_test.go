package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clientconfig "bd2server/internal/client/config"
)

func TestIndexRequiresSessionAndServesEmbeddedStudio(t *testing.T) {
	h := &handler{token: "test-session", origin: "http://127.0.0.1"}
	server := httptest.NewServer(h.routes())
	defer server.Close()

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("without session status=%d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/?session=test-session")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buffer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	page := string(buffer)
	for _, marker := range []string{
		"BD2 Client Studio", "test-session", `name="bd2-platform"`,
		`id="directoryScene"`, `id="serverScene"`, `id="deskScene"`,
		`id="gameDir"`, `id="origin"`, `id="patch"`, `id="install"`, `id="launch"`,
		`value="official"`, `value="local"`, `value="server"`,
		`id="localResourceDir"`, `id="browseResources"`,
		`Asia/Shanghai`, `Asia/Hong_Kong`, `Asia/Macau`, `Asia/Taipei`,
		`const zhCN=CHINA_TIME_ZONES.has(detectedTimeZone)`,
		"opening-curtain", "is-entering", "@keyframes reveal", "prefers-reduced-motion",
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page lacks %q", marker)
		}
	}
	if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("security headers=%v", response.Header)
	}
}

func TestAPIRejectsMalformedAndTrailingJSON(t *testing.T) {
	h := &handler{token: "test-session", origin: "http://local.invalid", versions: clientconfig.ReleaseVersions{ClientVersion: "2.35.10"}}
	for name, body := range map[string]string{
		"malformed": `{`,
		"trailing":  `{}` + `{}`,
		"unknown":   `{"unexpected":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/inspect", strings.NewReader(body))
			request.Header.Set("X-BD2-Session", "test-session")
			request.Header.Set("Origin", "http://local.invalid")
			response := httptest.NewRecorder()
			h.routes().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAPIRejectsMissingTokenAndForeignOrigin(t *testing.T) {
	h := &handler{token: "test-session", origin: "http://local.invalid", browse: func(string) (string, error) { return "", nil }}
	for name, values := range map[string][2]string{
		"missing token":  {"", "http://local.invalid"},
		"foreign origin": {"test-session", "https://attacker.invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/browse", strings.NewReader("{}"))
			request.Header.Set("X-BD2-Session", values[0])
			request.Header.Set("Origin", values[1])
			response := httptest.NewRecorder()
			h.routes().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestInspectAPI(t *testing.T) {
	dir := t.TempDir()
	for path, data := range map[string][]byte{
		filepath.Join(dir, "BrownDust II.exe"):                        []byte("exe"),
		filepath.Join(dir, "BrownDust II_Data", "resources.assets"):   []byte("not a real Unity file"),
		filepath.Join(dir, "BrownDust II_Data", "globalgamemanagers"): []byte("\x002.35.10\x00"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := &handler{token: "test-session", origin: "http://local.invalid", versions: clientconfig.ReleaseVersions{ClientVersion: "2.35.10"}}
	body, _ := json.Marshal(request{GameDirectory: dir})
	req := httptest.NewRequest(http.MethodPost, "/api/inspect", strings.NewReader(string(body)))
	req.Header.Set("X-BD2-Session", "test-session")
	req.Header.Set("Origin", "http://local.invalid")
	recorder := httptest.NewRecorder()
	h.routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result response
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("response=%+v", result)
	}
}
