package app

import (
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/i18n"
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/notice"
	"bd2server/internal/server/gateway/auth"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/gateway/transport"
	"bd2server/internal/server/platform/lifecycle"
	"bd2server/internal/server/platform/textmoderation"
	"bd2server/internal/server/platform/updates"
	identitystore "bd2server/internal/server/storage/identity"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func Serve(args []string) (serveErr error) {
	defer func() { serveErr = errors.Join(serveErr, gamedata.CloseDatabaseCache()) }()
	confirmation, err := startUpdatedVersion(args)
	if err != nil {
		return err
	}
	defer func() {
		var restart *updates.RestartError
		if serveErr != nil && confirmation != nil && !errors.As(serveErr, &restart) {
			code, err := confirmation.Failed(serveErr)
			serveErr = &updates.ExitError{Code: code, Err: err}
		}
	}()
	c, err := loadConfiguration(args)
	if err != nil {
		return err
	}
	defer clear(c.authRuntime.MasterKey)
	c.textModerator, err = textmoderation.New(c.gameRules.TextModeration, filepath.Dir(c.gameConfigPath))
	if err != nil {
		return fmt.Errorf("initialize text moderation: %w", err)
	}
	defer func() { serveErr = errors.Join(serveErr, c.textModerator.Close()) }()
	seeds, err := loadSeeds(c)
	if err != nil {
		return err
	}
	design, err := loadDesign(c, seeds)
	if err != nil {
		return err
	}
	var authService *auth.Service
	var authStore *identitystore.Store
	if c.authentication.Mode == "oauth" {
		authStore, err = identitystore.Open(filepath.Join(c.stateDirectory, "auth.db"), c.authRuntime.MasterKey)
		if err != nil {
			return fmt.Errorf("open authentication database: %w", err)
		}
		defer func() { serveErr = errors.Join(serveErr, authStore.Close()) }()
		authService, err = auth.New(c.authRuntime, authStore)
		if err != nil {
			return err
		}
	}

	if err := lockServerState(c.stateDirectory, c.gameRules.Story.StartPackID); err != nil {
		return err
	}
	c.gameFile.Lock(commonconfig.Lock{Path: "story.start_pack_id", Value: c.gameRules.Story.StartPackID, Reason: "首次初始化后已永久保存于服务器状态，不能再更改"})
	shared, err := openSharedRuntime(context.Background(), c, design)
	if err != nil {
		return err
	}
	defer func() { serveErr = errors.Join(serveErr, shared.store.Close()) }()
	factory := &PlayerFactory{options: c, design: design, seeds: seeds, pvpSource: shared.pvp, shared: shared, wakeOutbox: shared.wake}
	if authStore != nil {
		factory.profiles = authStore
	}
	registry := newPlayerRegistry(factory, 15*time.Minute)
	defer func() { serveErr = errors.Join(serveErr, registry.Close(context.Background())) }()
	discovery := outboxDiscovery{factory: factory}
	if authStore != nil {
		discovery.accounts = authStore
	}
	var authenticator session.LoginAuthenticator
	if authService != nil {
		authenticator = authService
	}
	game, err := session.NewServer(registry, authenticator)
	if err != nil {
		return err
	}

	var authHandler http.Handler
	if authService != nil {
		authHandler = authService.Handler()
	}
	availability := lifecycle.NewGate()
	instanceBytes := make([]byte, 16)
	if _, err := rand.Read(instanceBytes); err != nil {
		return fmt.Errorf("create server instance identity: %w", err)
	}
	instanceID := hex.EncodeToString(instanceBytes)
	realtime, err := openRealtime(c, registry, game, shared, instanceID)
	if err != nil {
		return err
	}
	defer func() { serveErr = errors.Join(serveErr, realtime.close()) }()
	if err := shared.start(registry, discovery); err != nil {
		return err
	}
	defer func() { serveErr = errors.Join(serveErr, shared.stop()) }()
	var realtimeDone <-chan error
	var realtimeTickets http.Handler
	if realtime != nil {
		realtime.start()
		realtimeDone = realtime.done
		realtimeTickets = realtime.tickets
	}
	console, consoleStore, err := openConsole(c, design, seeds, authService, authStore, instanceID)
	if err != nil {
		return fmt.Errorf("open administration console: %w", err)
	}
	defer func() { serveErr = errors.Join(serveErr, consoleStore.Close()) }()
	var stopOnce sync.Once
	stopForUpdate := make(chan struct{})
	updater := updates.New(updates.Options{
		Config: c.updates, DataDir: c.dataDir, StateDirs: []string{c.stateDirectory, filepath.Join(c.dataDir, "config")},
		ServerVersion: c.versions.ServerVersion, ServeArgs: c.serveArgs, Board: c.notices, BundledChangelog: c.versions.Resolve("CHANGELOG.md"),
		Shutdown: func() { stopOnce.Do(func() { close(stopForUpdate) }) },
	})
	console.AttachUpdates(updater)
	updateContext, stopUpdates := context.WithCancel(context.Background())
	defer stopUpdates()
	go updater.Run(updateContext)
	compatibility := c.compatibility
	go confirmation.AfterHealthy(updateContext, availability.Ready)
	game.Broadcast = func() []byte { return notice.Notify(c.notices.Sequence(), updater.MaintenanceStart()) }
	game.LoggedIn = func(account string, language uint64) { c.notices.SetLanguage(account, i18n.FromGameLanguage(language)) }
	loginsClosed := func(language uint64) string {
		_, message := updater.LoginsClosed(i18n.FromGameLanguage(language))
		return message
	}
	dispatcher := transport.Bootstrap{Config: c.bootstrap, Notices: c.notices, LoginsClosed: loginsClosed}
	handler := transport.HTTP{
		Dispatcher: dispatcher, Raw: game, Authentication: c.authentication,
		AuthenticationHandler: authHandler, ResourcePolicy: c.publicResources,
		CommerceManifest: func() any { return design.cashCatalog.Manifest() }, Availability: availability, InstanceID: instanceID, RealtimeTickets: realtimeTickets,
		StoryCheckpoints: storyCheckpoints{game: game, players: registry}, Console: console.Handler(),
		LoginsClosed:      func() bool { return loginsClosed(0) != "" },
		Releases:          releaseFiles(filepath.Join(c.dataDir, "releases")),
		ClientAllowed:     compatibility.allowed,
		UpdateFingerprint: func() []byte { return compatibility.envelope },
		Compatibility: func() any {
			minimum := c.versions.MinimumPlugins.ByID()
			delete(minimum, "capture_environment")
			return map[string]any{
				"server_version": c.versions.ServerVersion, "game_version": c.versions.GameVersion,
				"bundle_version": c.versions.BundleVersion, "game_data_version": c.versions.GameDataVersion,
				"fingerprint": compatibility.summary(), "fingerprint_url": "/client/update-fingerprint",
				"minimum_plugins": minimum, "maintenance_at": updater.MaintenanceStart(), "logins_closed": loginsClosed(0) != "",
			}
		},
	}.Handler()
	server := &http.Server{
		Addr:              c.listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	defer func() { _ = server.Close() }()
	slog.Info("BD2 server listening", "address", c.listen, "instance_id", instanceID, "server_version", c.versions.ServerVersion, "game_version", c.bootstrap.Version, "bundle", c.bootstrap.BundleVer, "resourceMode", c.publicResources.Mode, "gameData", c.verifiedGameData.ArchivePath, "gameDataEntries", c.verifiedGameData.EntryCount, "accountSeed", c.accountSeed)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.ListenAndServe() }()
	if link, err := console.IssueSignInLink(consoleOrigin(c)); err != nil {
		slog.Warn("administration console sign-in link unavailable", "error", err)
	} else {
		slog.Info("administration console owner sign-in link (single use, 24 hours)", "url", link)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, lifecycle.ShutdownSignals()...)
	defer signal.Stop(signals)
	select {
	case err := <-realtimeDone:
		return fmt.Errorf("realtime listener stopped: %w", err)
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case received := <-signals:
		slog.Info("BD2 server draining", "signal", received.String())
	case <-stopForUpdate:
		slog.Info("BD2 server draining for update")
	}
	availability.Drain()
	drainContext, cancelDrain := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelDrain()
	if err := availability.Wait(drainContext); err != nil {
		slog.Warn("BD2 request drain timed out", "error", err)
	}
	if err := server.Shutdown(drainContext); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown drained server: %w", err)
	}
	if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("BD2 server stopped after drain")
	if plan := updater.TakePlan(); plan != nil {
		return &updates.RestartError{Plan: plan}
	}
	return nil
}

// releaseFiles serves signed manifests and content-addressed files uploaded
// independently to DATA_DIR/releases.
func releaseFiles(directory string) http.Handler {
	files := http.StripPrefix("/releases/", http.FileServerFS(os.DirFS(directory)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			http.Error(w, "download unavailable", http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/releases/files/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// startUpdatedVersion runs before configuration loads, so that a new version
// whose configuration or stores fail to open is still rolled back.
func startUpdatedVersion(args []string) (*updates.Confirmation, error) {
	dataDir, versionPath := "", ""
	for i := 0; i < len(args); i++ {
		name, value, assigned := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if name != "data-dir" && name != "version-config" {
			continue
		}
		if !assigned && i+1 < len(args) {
			i++
			value = args[i]
		}
		if name == "data-dir" {
			dataDir = value
		} else {
			versionPath = value
		}
	}
	if dataDir == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		dataDir = filepath.Join(filepath.Dir(executable), "data")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	var versions versionconfig.Config
	if versionPath != "" {
		versions, err = versionconfig.Load(versionPath)
	} else {
		versions, err = versionconfig.Find()
	}
	if err != nil {
		return nil, err
	}
	// A container waiting for its replacement image must still stop promptly.
	ctx, stop := signal.NotifyContext(context.Background(), lifecycle.ShutdownSignals()...)
	defer stop()
	confirmation, code, err := updates.Startup(ctx, dataDir, versions.ServerVersion)
	if err != nil {
		return nil, &updates.ExitError{Code: code, Err: err}
	}
	return confirmation, nil
}
