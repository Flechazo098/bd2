package app

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clientconfig "bd2server/internal/client/config"
)

func TestEmbeddedStudioUsesNativeBindings(t *testing.T) {
	page, err := fs.ReadFile(webFS, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	for _, marker := range []string{
		"BD2 Client Studio", "window.go.app.Studio", "bridge().Initialize()",
		"(()=>{", "async function navigateScene(next)",
		`id="directoryScene"`, `id="serverScene"`, `id="deskScene"`,
		`id="patch"`, `id="install"`, `id="launch"`,
		`value="official"`, `value="local"`, `value="server"`,
		"prefers-reduced-motion",
		"opening-curtain", "is-entering", "@keyframes reveal", "bridge().Quit()",
		"Asia/Shanghai", "Asia/Hong_Kong", "Asia/Macau", "Asia/Taipei",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("embedded desktop interface lacks %q", marker)
		}
	}
	for _, obsolete := range []string{"fetch(", "/api/", "bd2-session", "X-BD2-Session", "{{.", "window_darwin.js", "function go("} {
		if strings.Contains(text, obsolete) {
			t.Errorf("embedded desktop interface retains obsolete browser bridge %q", obsolete)
		}
	}
}

func TestStudioInitializeLoadsSavedSettings(t *testing.T) {
	dir := makeTestClient(t)
	studio := NewStudio(Options{InitialGameDir: dir, Versions: clientconfig.ReleaseVersions{ClientVersion: "2.35.10"}}, NativeHost{})
	if state := studio.Initialize(); state.AutoOpen {
		t.Fatal("missing settings enabled automatic workspace")
	}
	if _, err := clientconfig.Save(dir, clientconfig.Settings{ServerOrigin: "https://play.example.com", CDNMode: clientconfig.CDNOfficial}); err != nil {
		t.Fatal(err)
	}
	state := studio.Initialize()
	if !state.AutoOpen || state.GameDirectory != dir || state.ServerOrigin != "https://play.example.com" {
		t.Fatalf("state=%+v", state)
	}
}

func TestStudioSaveRemembersOnlyValidatedSettings(t *testing.T) {
	dir := makeTestClient(t)
	studio := NewStudio(Options{Versions: clientconfig.ReleaseVersions{ClientVersion: "2.35.10"}}, NativeHost{})
	var remembered string
	studio.savePreferences = func(value string) error { remembered = value; return nil }
	input := Request{GameDirectory: dir, ServerOrigin: "https://play.example.com", CDNMode: clientconfig.CDNOfficial}
	result, err := studio.Save(input)
	if err != nil || !result.OK || remembered != dir {
		t.Fatalf("result=%+v remembered=%q err=%v", result, remembered, err)
	}
	remembered = ""
	input.ServerOrigin = "http://untrusted.example.com"
	if _, err := studio.Save(input); err == nil || remembered != "" {
		t.Fatalf("invalid settings remembered=%q err=%v", remembered, err)
	}
	if _, err := studio.Launch(Request{GameDirectory: dir}); err == nil {
		t.Fatal("unpatched game launch accepted")
	}
}

func TestStudioContextCancellationAndPickerCancellation(t *testing.T) {
	studio := NewStudio(Options{}, NativeHost{BrowseDirectory: func(context.Context, string) (string, error) { return "", nil }})
	for _, browse := range []func(Request) (Response, error){studio.Browse, studio.BrowseResources} {
		result, err := browse(Request{})
		if err != nil || !result.OK || result.Message != "Selection cancelled" || result.Data != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	studio.Startup(ctx)
	cancel()
	if _, err := studio.Resources(Request{ServerOrigin: "http://127.0.0.1:8080", CDNMode: clientconfig.CDNOfficial}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestStudioShutdownCancelsOwnedContext(t *testing.T) {
	studio := NewStudio(Options{}, NativeHost{})
	studio.Startup(context.Background())
	owned := studio.context()
	studio.Shutdown()
	if !errors.Is(owned.Err(), context.Canceled) {
		t.Fatalf("owned context err=%v", owned.Err())
	}
}

func TestStudioInitializeDefaultsWithoutGameDirectory(t *testing.T) {
	studio := NewStudio(Options{LogPath: "test.log"}, NativeHost{})
	studio.Startup(context.Background())
	state := studio.Initialize()
	if state.Platform == "" || state.LogPath != "test.log" || state.GameDirectory != "" {
		t.Fatalf("state=%+v", state)
	}
	if state.ServerOrigin != "http://127.0.0.1:8080" || state.CDNMode != clientconfig.CDNOfficial || state.AutoOpen {
		t.Fatalf("defaults=%+v", state)
	}
}

func TestStudioBrowseUsesNativeDirectoryPicker(t *testing.T) {
	dir := makeTestClient(t)
	var title string
	studio := NewStudio(Options{Versions: clientconfig.ReleaseVersions{ClientVersion: "2.35.10"}}, NativeHost{
		BrowseDirectory: func(_ context.Context, requested string) (string, error) {
			title = requested
			return dir, nil
		},
	})
	studio.Startup(context.Background())
	result, err := studio.Browse(Request{UILanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Data == nil || title != "选择 Brown Dust II 安装目录" {
		t.Fatalf("result=%+v title=%q", result, title)
	}
}

func TestStudioQuitUsesNativeLifecycleOnce(t *testing.T) {
	quits := 0
	studio := NewStudio(Options{}, NativeHost{Quit: func(context.Context) { quits++ }})
	studio.Startup(context.Background())
	studio.Quit()
	studio.Quit()
	if quits != 1 {
		t.Fatalf("quits=%d", quits)
	}
}

func TestStudioPropagatesOperationErrors(t *testing.T) {
	want := errors.New("picker failed")
	studio := NewStudio(Options{}, NativeHost{BrowseDirectory: func(context.Context, string) (string, error) {
		return "", want
	}})
	studio.Startup(context.Background())
	if _, err := studio.Browse(Request{}); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestDesktopSecurityHeaders(t *testing.T) {
	called := false
	handler := desktopSecurityHeaders(httpHandlerFunc(func(header map[string]string) {
		called = true
		if header["Cache-Control"] != "no-store" || header["Content-Security-Policy"] == "" {
			t.Fatalf("headers=%v", header)
		}
	}))
	response := &headerRecorder{header: make(map[string][]string)}
	handler.ServeHTTP(response, nil)
	if !called {
		t.Fatal("asset middleware did not call the next handler")
	}
}

// Small local adapters keep this middleware test independent of httptest's
// network-shaped helpers.
type httpHandlerFunc func(map[string]string)

func (f httpHandlerFunc) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	headers := make(map[string]string)
	for name, values := range w.Header() {
		if len(values) != 0 {
			headers[name] = values[0]
		}
	}
	f(headers)
}

type headerRecorder struct{ header http.Header }

func (r *headerRecorder) Header() http.Header     { return r.header }
func (*headerRecorder) Write([]byte) (int, error) { return 0, nil }
func (*headerRecorder) WriteHeader(int)           {}

func makeTestClient(t *testing.T) string {
	t.Helper()
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
	return dir
}
