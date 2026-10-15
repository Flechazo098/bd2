package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bd2server/internal/common/release"
	"bd2server/internal/common/versionconfig"
)

func TestDownloadPinsManifestAndExcludesCompetingOperations(t *testing.T) {
	root := t.TempDir()
	component := &release.Component{Version: "2.36.7+client.0.2.0", Files: []release.File{releaseFile(launcherExecutable(), "new executable")}}
	raw, keys := signedFixture(t, component, nil)
	entered, resume := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	var checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			checks.Add(1)
			_, _ = w.Write(raw)
			return
		}
		close(entered)
		<-resume
		_, _ = w.Write([]byte("new executable"))
	}))
	defer server.Close()
	defer unblock.Do(func() { close(resume) })
	m := New(Options{Directory: root, Versions: versionconfig.Config{ClientVersion: "2.36.7+client.0.1.0"}})
	m.manifestURL, m.keys = server.URL+"/manifest.json", keys
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Check(ctx); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() { completed <- m.Download(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("download never started")
	}
	for name, action := range map[string]func() error{
		"check":    func() error { return m.Check(ctx) },
		"download": func() error { return m.Download(ctx) },
		"commit":   m.CommitPlugins,
		"apply":    func() error { return m.ApplyLauncher(func() { t.Error("quit during another operation") }) },
	} {
		if err := action(); err == nil || !strings.Contains(err.Error(), "in progress") {
			t.Errorf("%s competed with a download: %v", name, err)
		}
	}
	if checks.Load() != 1 {
		t.Errorf("competing check fetched a new manifest: %d requests", checks.Load())
	}
	snapshot := m.Status()
	snapshot.Launcher.Version, snapshot.Launcher.Ready = "mutated", true
	if actual := m.Status().Launcher; actual.Version != component.Version || actual.Ready {
		t.Errorf("status caller changed active offer: %+v", actual)
	}
	unblock.Do(func() { close(resume) })
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if state := m.Status(); state.Busy != "" || !state.Launcher.Ready {
		t.Fatalf("download did not finish its original offer: %+v", state)
	}
	writeFixture(t, filepath.Join(m.launcherStaging(component.Version), launcherExecutable()), "tampered bytes")
	quit := false
	if err := m.ApplyLauncher(func() { quit = true }); err == nil || quit {
		t.Fatalf("accepted staged file changed after Ready: quit=%v, err=%v", quit, err)
	}
}

func TestPluginOnlyRemovalStillOffersAndAppliesTheNewSet(t *testing.T) {
	root := t.TempDir()
	keep := releaseFile("BD2LoginUI.dll", "same login")
	component := &release.Component{Version: "2.36.7+client.0.2.0", Files: []release.File{keep}}
	raw, keys := signedFixture(t, nil, component)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	defer server.Close()
	m := New(Options{Directory: root, Versions: versionconfig.Config{ClientVersion: "2.36.7+client.0.1.0"}})
	m.manifestURL, m.keys = server.URL+"/manifest.json", keys
	writeFixture(t, filepath.Join(root, "plugins", keep.Name), "same login")
	writeFixture(t, filepath.Join(root, "plugins", "BD2FChat.dll"), "old chat")
	store := Store{Version: "2.36.7+client.0.1.0", Files: []StoredFile{{Name: keep.Name, SHA256: keep.SHA256}, {Name: "BD2FChat.dll"}}}
	encoded, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "plugins", StoreFile), string(encoded))
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := m.Status(); state.Plugins == nil || !state.Plugins.Ready || state.Plugins.Bytes != 0 {
		t.Fatalf("removing a plugin should require no new download: %+v", state)
	}
	if err := m.CommitPlugins(); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(root, "plugins", keep.Name), "same login")
	if _, err := os.Stat(filepath.Join(root, "plugins", "BD2FChat.dll")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dropped plugin remains: %v", err)
	}
	store, err = ReadStore(root)
	if err != nil || store.Version != component.Version || len(store.Files) != 1 {
		t.Fatalf("store = %+v, %v", store, err)
	}
}

func TestUpdateWaitingProcess(t *testing.T) {
	if marker := os.Getenv("BD2_UPDATE_TEST_PROCESS_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
	}
}

func startWaitingProcess(t *testing.T, path string) (int, func()) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(executable, path); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "child-ready")
	child := exec.Command(path, "-test.run=^TestUpdateWaitingProcess$")
	child.Env = append(os.Environ(), "BD2_UPDATE_TEST_PROCESS_MARKER="+marker)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = input.Close()
			select {
			case err := <-waited:
				if err != nil {
					t.Errorf("fixture process exited unsuccessfully: %v", err)
				}
			case <-time.After(10 * time.Second):
				_ = child.Process.Kill()
				<-waited
				t.Error("fixture process did not exit after stdin closed")
			}
		})
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture process failed to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return child.Process.Pid, stop
}

func TestPluginUpdateWaitsForRunningGameExitAndPreservesPlayerConfig(t *testing.T) {
	root := t.TempDir()
	game := filepath.Join(root, "game")
	writeFixture(t, filepath.Join(game, "BrownDust II_Data", "resources.assets"), "fixture")
	writeFixture(t, filepath.Join(game, "BepInEx", "core", "BepInEx.dll"), "fixture")
	pluginPath := filepath.Join(game, "BepInEx", "plugins", "BD2LoginUI.dll")
	writeFixture(t, pluginPath, "old login")
	configPath := filepath.Join(game, "BepInEx", "config", "BD2LoginUI.cfg")
	writeFixture(t, configPath, "player settings")
	_, stopGame := startWaitingProcess(t, filepath.Join(game, "BrownDust II.exe"))
	f := releaseFile("BD2LoginUI.dll", "new login")
	component := &release.Component{Version: "2.36.7+client.0.2.0", Files: []release.File{f}}
	raw, keys := signedFixture(t, nil, component)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(raw)
		case "/" + f.Name:
			downloads.Add(1)
			_, _ = w.Write([]byte("new login"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := New(Options{Directory: root, Games: func() string { return game }, Versions: versionconfig.Config{ClientVersion: "2.36.7+client.0.1.0"}})
	m.manifestURL, m.keys = server.URL+"/manifest.json", keys
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if offer := m.Status().Plugins; offer == nil || offer.Ready || offer.Version != component.Version {
		t.Fatalf("signed plugin manifest did not offer a download: %+v", offer)
	}
	if err := m.Download(ctx); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("plugin artifact downloads = %d; want 1", downloads.Load())
	}
	assertContent(t, filepath.Join(m.pluginStaging(component.Version), f.Name), "new login")
	if err := m.CommitPlugins(); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(root, "plugins", f.Name), "new login")
	store, err := ReadStore(root)
	if err != nil || store.Version != component.Version || len(store.Files) != 1 || store.Files[0].SHA256 != f.SHA256 {
		t.Fatalf("committed plugin store = %+v, %v", store, err)
	}
	if waiting := m.Status().Waiting; len(waiting) != 1 || waiting[0] != game {
		t.Fatalf("running game was not queued for installation: %v", waiting)
	}
	if err := m.EnsurePlugins(game); err == nil || !strings.Contains(err.Error(), "game is running") {
		t.Fatalf("installation did not reject the running game: %v", err)
	}
	m.installWaiting()
	assertContent(t, pluginPath, "old login")
	assertContent(t, configPath, "player settings")
	if waiting := m.Status().Waiting; len(waiting) != 1 || waiting[0] != game {
		t.Fatalf("running game lost its pending installation: %v", waiting)
	}
	stopGame()
	m.installWaiting()
	assertContent(t, pluginPath, "new login")
	assertContent(t, configPath, "player settings")
	if waiting := m.Status().Waiting; len(waiting) != 0 {
		t.Fatalf("installed game remains pending after exit: %v", waiting)
	}
}

func TestWithLaunchKeepsUpdatesExcludedThroughProcessStartAndReturnsLaunchError(t *testing.T) {
	root := t.TempDir()
	game := filepath.Join(root, "game")
	writeFixture(t, filepath.Join(game, "BrownDust II.exe"), "fixture")
	writeFixture(t, filepath.Join(game, "BrownDust II_Data", "resources.assets"), "fixture")
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	m := New(Options{Directory: root})
	failure := errors.New("process start failed")
	called := false
	err := m.WithLaunch(context.Background(), server.Client(), game, server.URL, func() error {
		called = true
		if !m.Busy() {
			t.Error("operation was released before process start")
		}
		if err := m.CommitPlugins(); err == nil {
			t.Error("plugin commit started during process launch")
		}
		return failure
	})
	if !called || !errors.Is(err, failure) {
		t.Fatalf("launch callback = %v, %v", called, err)
	}
	if m.Busy() {
		t.Error("failed process launch left updates blocked")
	}
	if err := m.CommitPlugins(); err != nil {
		t.Fatalf("updates cannot resume after launch failure: %v", err)
	}
}

func TestServerCompatibilityRejectsMissingPluginsAndTemporaryLoginClosure(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		compat   Compatibility
		version  string
		expected string
	}{
		{name: "missing plugin", compat: Compatibility{ServerVersion: "2.36.7+server.0.2.0", MinimumPlugins: map[string]string{"login-ui": "1.2.0"}}, expected: "login-ui"},
		{name: "login closed", compat: Compatibility{LoginsClosed: true}, expected: "server is updating"},
		{name: "game version", compat: Compatibility{GameVersion: "2.37.0"}, version: "2.36.7", expected: "does not match"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(scenario.compat); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			m := New(Options{Directory: t.TempDir(), Versions: versionconfig.Config{GameVersion: scenario.version}})
			if err := m.CheckServer(context.Background(), server.Client(), server.URL); err == nil || !strings.Contains(err.Error(), scenario.expected) {
				t.Fatalf("server compatibility accepted incompatible launch: %v", err)
			}
		})
	}
}
