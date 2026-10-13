package app

import (
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/gateway/auth"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/gateway/transport"
	"bd2server/internal/server/platform/lifecycle"
	"bd2server/internal/server/platform/textmoderation"
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
	"time"
)

func Serve(args []string) (serveErr error) {
	defer func() { serveErr = errors.Join(serveErr, gamedata.CloseDatabaseCache()) }()
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
	dispatcher := transport.Bootstrap{Config: c.bootstrap}
	handler := transport.HTTP{
		Dispatcher: dispatcher, Raw: game, Authentication: c.authentication,
		AuthenticationHandler: authHandler, ResourcePolicy: c.publicResources,
		CommerceManifest: func() any { return design.cashCatalog.Manifest() }, Availability: availability, InstanceID: instanceID, RealtimeTickets: realtimeTickets,
		StoryCheckpoints: storyCheckpoints{game: game, players: registry}, Console: console.Handler(),
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
	return nil
}
