// Package app serves bd2client's embedded, loopback-only setup interface.
package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	clientconfig "bd2server/internal/client/config"
	clientlayout "bd2server/internal/client/layout"
	clientsetup "bd2server/internal/client/setup"
)

//go:embed web/index.html
var webFS embed.FS

var errGameAlreadyRunning = errors.New("Brown Dust II is already running")

type Options struct {
	Listen              string
	NoBrowser           bool
	InitialGameDir      string
	Logger              *slog.Logger
	LogPath             string
	Versions            clientconfig.ReleaseVersions
	LocalIdentityPlugin string
	LoginUIPlugin       string
}

type request struct {
	GameDirectory          string               `json:"game_directory"`
	ServerOrigin           string               `json:"server_origin"`
	CDNMode                clientconfig.CDNMode `json:"cdn_mode"`
	LocalResourceDirectory string               `json:"local_resource_directory"`
	UILanguage             string               `json:"ui_language"`
}

type response struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

type handler struct {
	token               string
	origin              string
	initialGameDir      string
	browse              func(string) (string, error)
	browseResources     func(string) (string, error)
	shutdown            func()
	quitOnce            sync.Once
	logger              *slog.Logger
	logPath             string
	versions            clientconfig.ReleaseVersions
	initialSettings     clientconfig.Settings
	autoOpen            bool
	localIdentityPlugin string
	loginUIPlugin       string
}

func Run(options Options) error {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	listen := options.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		logger.Error("could not start local interface", "error", err)
		return fmt.Errorf("start bd2client interface: %w", err)
	}
	address := listener.Addr().(*net.TCPAddr)
	if !address.IP.IsLoopback() {
		_ = listener.Close()
		logger.Error("refused non-loopback interface", "address", listener.Addr().String())
		return errors.New("bd2client interface must listen on a loopback address")
	}
	token, err := newToken()
	if err != nil {
		_ = listener.Close()
		logger.Error("could not create local interface session", "error", err)
		return err
	}
	origin := "http://" + listener.Addr().String()
	initialSettings := clientconfig.Settings{ServerOrigin: "http://127.0.0.1:8080", CDNMode: clientconfig.CDNOfficial}
	autoOpen := false
	if options.InitialGameDir != "" {
		if _, inspectErr := clientsetup.Inspect(options.InitialGameDir, options.Versions); inspectErr != nil {
			logger.Warn("saved game directory is no longer valid", "error", inspectErr)
		} else if loaded, loadErr := clientconfig.Load(options.InitialGameDir); loadErr != nil {
			logger.Warn("saved client connection settings are unavailable", "error", loadErr)
		} else {
			initialSettings = loaded
			autoOpen = true
		}
	}
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	done := make(chan struct{})
	h := &handler{
		token:               token,
		origin:              origin,
		initialGameDir:      options.InitialGameDir,
		browse:              browseForGameDirectory,
		browseResources:     browseForResourceDirectory,
		logger:              logger,
		logPath:             options.LogPath,
		versions:            options.Versions,
		initialSettings:     initialSettings,
		autoOpen:            autoOpen,
		localIdentityPlugin: options.LocalIdentityPlugin,
		loginUIPlugin:       options.LoginUIPlugin,
		shutdown: func() {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = server.Shutdown(ctx)
			}()
		},
	}
	server.Handler = h.routes()
	logger.Info("local interface listening", "address", listener.Addr().String())
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("local interface stopped unexpectedly", "error", err)
		}
		close(done)
	}()
	pageURL := origin + "/?session=" + token
	fmt.Fprintf(os.Stdout, "BD2 Client Studio: %s\n", pageURL)
	if !options.NoBrowser {
		if err := openBrowser(pageURL); err != nil {
			logger.Warn("could not open client window automatically", "error", err)
		} else {
			logger.Info("client window opened")
		}
	} else {
		logger.Info("automatic client window disabled")
	}
	<-done
	logger.Info("local interface stopped")
	return nil
}

func (h *handler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.index)
	mux.HandleFunc("POST /api/browse", h.observe("browse game directory", h.authorize(h.browseDirectory)))
	mux.HandleFunc("POST /api/browse-resources", h.observe("browse resource directory", h.authorize(h.browseResourceDirectory)))
	mux.HandleFunc("POST /api/inspect", h.observe("inspect game directory", h.authorize(h.inspect)))
	mux.HandleFunc("POST /api/resources", h.observe("check resource policy", h.authorize(h.resources)))
	mux.HandleFunc("POST /api/save", h.observe("save settings", h.authorize(h.save)))
	mux.HandleFunc("POST /api/patch", h.observe("patch client", h.authorize(h.patch)))
	mux.HandleFunc("POST /api/install", h.observe("install plugins", h.authorize(h.install)))
	mux.HandleFunc("POST /api/launch", h.observe("launch game", h.authorize(h.launch)))
	mux.HandleFunc("POST /api/quit", h.observe("quit", h.authorize(h.quit)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		mux.ServeHTTP(w, r)
	})
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || r.URL.Query().Get("session") != h.token {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "embedded interface unavailable", http.StatusInternalServerError)
		return
	}
	tmpl, err := template.New("index").Parse(string(data))
	if err != nil {
		http.Error(w, "embedded interface invalid", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; connect-src 'self'; font-src 'self'")
	_ = tmpl.Execute(w, map[string]string{
		"Token": h.token, "GameDirectory": h.initialGameDir, "LogPath": h.logPath, "Platform": runtime.GOOS,
		"ServerOrigin": h.initialSettings.ServerOrigin, "CDNMode": string(h.initialSettings.CDNMode),
		"LocalResourceDirectory": h.initialSettings.LocalResourceDirectory,
		"AutoOpen":               fmt.Sprintf("%t", h.autoOpen),
	})
}

func (h *handler) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-BD2-Session")
		if len(provided) != len(h.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.token)) != 1 {
			h.log().Warn("API request rejected", "operation", r.URL.Path, "reason", "invalid session")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != h.origin {
			h.log().Warn("API request rejected", "operation", r.URL.Path, "reason", "foreign origin")
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (h *handler) observe(operation string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracked := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next(tracked, r)
		if tracked.status >= http.StatusBadRequest {
			h.log().Error("client API operation failed", "operation", operation, "status", tracked.status)
			return
		}
		h.log().Info("client API operation completed", "operation", operation, "status", tracked.status)
	}
}

func (h *handler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

func (h *handler) browseDirectory(w http.ResponseWriter, r *http.Request) {
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	h.log().Info("directory selection opened", "kind", "game")
	dir, err := h.browse(input.UILanguage)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if dir == "" {
		h.log().Info("directory selection cancelled", "kind", "game")
		h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Selection cancelled"})
		return
	}
	status, err := clientsetup.Inspect(dir, h.versions)
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Game client found", Data: status})
}

func (h *handler) browseResourceDirectory(w http.ResponseWriter, r *http.Request) {
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	h.log().Info("directory selection opened", "kind", "resources")
	dir, err := h.browseResources(input.UILanguage)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if dir == "" {
		h.log().Info("directory selection cancelled", "kind", "resources")
		h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Selection cancelled"})
		return
	}
	policy, err := clientsetup.FetchResourcePolicy(context.Background(), nil, clientconfig.Settings{
		ServerOrigin:           "http://127.0.0.1",
		CDNMode:                clientconfig.CDNLocal,
		LocalResourceDirectory: dir,
	}, h.versions)
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Local resource directory found", Data: policy})
}

func (h *handler) inspect(w http.ResponseWriter, r *http.Request) {
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	status, err := clientsetup.Inspect(input.GameDirectory, h.versions)
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Game directory is valid", Data: status})
}

func (h *handler) resources(w http.ResponseWriter, r *http.Request) {
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	policy, err := clientsetup.FetchResourcePolicy(ctx, nil, input.settings(), h.versions)
	if err != nil {
		h.writeError(w, err)
		return
	}
	message := "The client will use the release-locked official CDN"
	if policy.Mode == clientconfig.CDNLocal {
		message = "Local resources verified"
	} else if policy.Mode == clientconfig.CDNServer {
		message = "Server resource policy verified"
	}
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: message, Data: policy})
}

func (h *handler) save(w http.ResponseWriter, r *http.Request) {
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	settings, err := clientsetup.SaveSettings(input.GameDirectory, input.settings(), h.versions)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if err := clientconfig.SavePreferences(input.GameDirectory); err != nil {
		h.writeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Connection settings saved", Data: settings})
}

func (h *handler) patch(w http.ResponseWriter, r *http.Request) {
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	result, err := clientsetup.Patch(input.GameDirectory, input.settings(), h.versions)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if err := clientconfig.SavePreferences(input.GameDirectory); err != nil {
		h.writeError(w, err)
		return
	}
	message := "Client entry point patched; the original backup was retained"
	if !result.Changed {
		message = "Client patch is already complete; no asset file was rewritten"
	}
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: message, Data: result})
}

func (h *handler) install(w http.ResponseWriter, r *http.Request) {
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	result, err := clientsetup.InstallPlugins(
		input.GameDirectory,
		input.settings(),
		h.versions,
		h.localIdentityPlugin,
		h.loginUIPlugin,
	)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if err := clientconfig.SavePreferences(input.GameDirectory); err != nil {
		h.writeError(w, err)
		return
	}
	message := "BD2 client plugins installed or updated"
	if !result.LocalIdentity.Changed && !result.LoginUI.Changed {
		message = "BD2 client plugins are already up to date; no DLL was rewritten"
	}
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: message, Data: result})
}

func (h *handler) launch(w http.ResponseWriter, r *http.Request) {
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	status, err := clientsetup.Inspect(input.GameDirectory, h.versions)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if status.PatchedURL != clientsetup.PatchPlaceholder {
		h.writeError(w, errors.New("apply the client patch before launching the game"))
		return
	}
	if !status.BepInEx {
		h.writeError(w, errors.New("install BepInEx before launching the game"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if _, err := clientsetup.FetchResourcePolicy(ctx, nil, input.settings(), h.versions); err != nil {
		h.writeError(w, err)
		return
	}
	if _, err := clientsetup.SaveSettings(input.GameDirectory, input.settings(), h.versions); err != nil {
		h.writeError(w, err)
		return
	}
	if err := clientconfig.SavePreferences(input.GameDirectory); err != nil {
		h.writeError(w, err)
		return
	}
	installation, err := clientlayout.Resolve(status.GameDirectory)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if !installation.SupportedOnHost() {
		h.writeError(w, fmt.Errorf("cannot launch a %s game client from this operating system", installation.Kind))
		return
	}
	for _, pluginName := range []string{"BD2LocalIdentity.dll", "BD2LoginUI.dll"} {
		info, statErr := os.Stat(filepath.Join(installation.Plugins, pluginName))
		if statErr != nil || !info.Mode().IsRegular() {
			h.writeError(w, fmt.Errorf("install or update the client plugins before launching; %s is missing", pluginName))
			return
		}
	}
	if err := launchGame(installation.LaunchTarget()); err != nil {
		if errors.Is(err, errGameAlreadyRunning) {
			h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Brown Dust II is already running"})
			return
		}
		h.writeError(w, fmt.Errorf("launch Brown Dust II: %w", err))
		return
	}
	h.log().Info("game launch requested", "platform", installation.Kind, "client_version", status.ClientVersion)
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Brown Dust II started"})
}

func (h *handler) quit(w http.ResponseWriter, _ *http.Request) {
	h.log().Info("client exit requested")
	h.writeJSON(w, http.StatusOK, response{OK: true, Message: "Client tool exited"})
	h.quitOnce.Do(h.shutdown)
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request) (request, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input request
	if err := decoder.Decode(&input); err != nil {
		h.writeError(w, fmt.Errorf("invalid request: %w", err))
		return request{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		h.writeError(w, errors.New("request must contain exactly one JSON object"))
		return request{}, false
	}
	return input, true
}

func (r request) settings() clientconfig.Settings {
	return clientconfig.Settings{
		ServerOrigin:           r.ServerOrigin,
		CDNMode:                r.CDNMode,
		LocalResourceDirectory: r.LocalResourceDirectory,
	}
}

func (h *handler) writeError(w http.ResponseWriter, err error) {
	h.log().Error("client operation error", "error", err)
	h.writeJSON(w, http.StatusBadRequest, response{OK: false, Message: err.Error()})
}

func (h *handler) writeJSON(w http.ResponseWriter, status int, value response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate UI session: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
