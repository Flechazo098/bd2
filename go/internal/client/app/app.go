// Package app exposes desktop business operations through native Go bindings.
package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	clientconfig "bd2server/internal/client/config"
	clientlayout "bd2server/internal/client/layout"
	clientplugin "bd2server/internal/client/plugin"
	clientsetup "bd2server/internal/client/setup"
	clientupdate "bd2server/internal/client/update"
	"bd2server/internal/common/versionconfig"
)

//go:embed web/index.html web/icon.svg web/icon.png web/icon.ico web/lang/*.json
var webFS embed.FS

var errGameAlreadyRunning = errors.New("Brown Dust II is already running") //nolint:staticcheck // ST1005

type Options struct {
	InitialGameDir   string
	Logger           *slog.Logger
	LogPath          string
	Versions         versionconfig.Config
	Development      bool
	PackageDirectory string
	DownloadManifest string
	Plugins          clientplugin.Set
}

type Request struct {
	ProxyURL               string               `json:"proxy_url"`
	GameDirectory          string               `json:"game_directory"`
	ServerOrigin           string               `json:"server_origin"`
	CDNMode                clientconfig.CDNMode `json:"cdn_mode"`
	LocalResourceDirectory string               `json:"local_resource_directory"`
	// DialogTitle is the folder picker's title in the UI language.
	DialogTitle string `json:"dialog_title"`
}

func (r Request) settings() clientconfig.Settings {
	return clientconfig.Settings{ServerOrigin: r.ServerOrigin, ProxyURL: r.ProxyURL, CDNMode: r.CDNMode, LocalResourceDirectory: r.LocalResourceDirectory}
}

type Response struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

type InitialState struct {
	ProxyURL               string               `json:"proxy_url"`
	Platform               string               `json:"platform"`
	ClientVersion          string               `json:"client_version"`
	GameVersion            string               `json:"game_version"`
	LogPath                string               `json:"log_path"`
	GameDirectory          string               `json:"game_directory"`
	ServerOrigin           string               `json:"server_origin"`
	CDNMode                clientconfig.CDNMode `json:"cdn_mode"`
	LocalResourceDirectory string               `json:"local_resource_directory"`
	AutoOpen               bool                 `json:"auto_open"`
}

type NativeHost struct {
	BrowseDirectory func(context.Context, string) (string, error)
	Quit            func(context.Context)
}

type Studio struct {
	options         Options
	host            NativeHost
	ctx             context.Context
	ctxMu           sync.RWMutex
	cancel          context.CancelFunc
	mu              sync.Mutex
	quitOnce        sync.Once
	launch          func(string, string, *slog.Logger) error
	savePreferences func(string) error
	updates         *clientupdate.Manager
}

func NewStudio(options Options, host NativeHost) *Studio {
	s := &Studio{options: options, host: host, ctx: context.Background(), launch: launchGame, savePreferences: clientconfig.SavePreferences}
	if executable, err := os.Executable(); err == nil {

		s.updates = clientupdate.New(clientupdate.Options{
			Versions: options.Versions, Directory: filepath.Dir(executable), Logger: options.Logger, Development: options.Development,
			DevelopmentPlugins: options.Plugins, ProjectDirectory: options.PackageDirectory,
			Games: func() string {
				preferences, err := clientconfig.LoadPreferences()
				if err != nil {
					return ""
				}
				return preferences.GameDirectory
			},
		})
	}
	return s
}

func (s *Studio) Startup(ctx context.Context) {
	owned, cancel := context.WithCancel(ctx)
	s.ctxMu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.ctx, s.cancel = owned, cancel
	s.ctxMu.Unlock()
	if s.updates != nil {
		go s.updates.Run(owned)
	}
}

func (s *Studio) Shutdown() {
	s.ctxMu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.ctxMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Studio) context() context.Context {
	s.ctxMu.RLock()
	ctx := s.ctx
	s.ctxMu.RUnlock()
	return ctx
}

func (s *Studio) log() *slog.Logger {
	if s.options.Logger != nil {
		return s.options.Logger
	}
	return slog.Default()
}

func (s *Studio) Initialize() InitialState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := InitialState{Platform: runtime.GOOS, ClientVersion: s.options.Versions.ClientVersion, GameVersion: s.options.Versions.GameVersion, LogPath: s.options.LogPath, GameDirectory: s.options.InitialGameDir, ServerOrigin: "http://127.0.0.1:8080", CDNMode: clientconfig.CDNOfficial}
	s.installationDefaults(&state)
	if s.options.InitialGameDir == "" {
		return state
	}
	if _, err := clientsetup.Inspect(state.GameDirectory, s.versions(state.GameDirectory)); err != nil {
		s.log().Warn("saved game directory is no longer valid", "error", err)
		return state
	}
	settings, err := clientconfig.Load(state.GameDirectory)
	if err != nil {
		s.log().Warn("saved client connection settings are unavailable", "error", err)
		return state
	}
	state.ProxyURL = settings.ProxyURL
	state.ServerOrigin = settings.ServerOrigin
	state.CDNMode = settings.CDNMode
	state.LocalResourceDirectory = settings.LocalResourceDirectory
	state.AutoOpen = true
	return state
}

func (s *Studio) perform(operation string, run func() (Response, error)) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.context().Err(); err != nil {
		return Response{}, err
	}
	result, err := run()
	if err != nil {
		s.log().Error("client operation failed", "operation", operation, "error", err)
		return Response{}, err
	}
	s.log().Info("client operation completed", "operation", operation)
	return result, nil
}

func success(message string, data any) Response {
	return Response{OK: true, Message: message, Data: data}
}

func (s *Studio) chooseDirectory(title, kind string) (string, error) {
	if s.host.BrowseDirectory == nil {
		return "", errors.New("native directory picker is unavailable")
	}
	prompt := title
	if prompt == "" {
		prompt = "Select the Brown Dust II installation directory"
		if kind == "resources" {
			prompt = "Select the CDN directory containing ServerData and GameData"
		}
	}
	s.log().Info("directory selection opened", "kind", kind)
	dir, err := s.host.BrowseDirectory(s.context(), prompt)
	if err == nil && dir == "" {
		s.log().Info("directory selection cancelled", "kind", kind)
	}
	return dir, err
}

func (s *Studio) Browse(input Request) (Response, error) {
	return s.perform("browse game directory", func() (Response, error) {
		dir, err := s.chooseDirectory(input.DialogTitle, "game")
		if err != nil {
			return Response{}, err
		}
		if dir == "" {
			return success("Selection cancelled", nil), nil
		}
		return success("Installation directory selected", clientsetup.GameStatus{GameDirectory: dir}), nil
	})
}

func (s *Studio) BrowseResources(input Request) (Response, error) {
	return s.perform("browse resource directory", func() (Response, error) {
		dir, err := s.chooseDirectory(input.DialogTitle, "resources")
		if err != nil {
			return Response{}, err
		}
		if dir == "" {
			return success("Selection cancelled", nil), nil
		}
		policy, err := clientsetup.FetchResourcePolicy(s.context(), nil, clientconfig.Settings{ServerOrigin: "http://127.0.0.1", CDNMode: clientconfig.CDNLocal, LocalResourceDirectory: dir}, s.versions(input.GameDirectory))
		return success("Local resource directory found", policy), err
	})
}

func (s *Studio) Inspect(input Request) (Response, error) {
	return s.perform("inspect game directory", func() (Response, error) {
		status, err := clientsetup.Inspect(input.GameDirectory, s.versions(input.GameDirectory))
		return success("Game directory is valid", status), err
	})
}

func (s *Studio) Resources(input Request) (Response, error) {
	return s.perform("check resource policy", func() (Response, error) {
		ctx, cancel := context.WithTimeout(s.context(), 12*time.Second)
		defer cancel()
		policy, err := clientsetup.FetchResourcePolicy(ctx, nil, input.settings(), s.versions(input.GameDirectory))
		message := "The client will use the release-locked official CDN"
		if policy.Mode == clientconfig.CDNLocal { //nolint:staticcheck // QF1003
			message = "Local resources verified"
		} else if policy.Mode == clientconfig.CDNServer {
			message = "Server resource policy verified"
		}
		return success(message, policy), err
	})
}

func (s *Studio) Save(input Request) (Response, error) {
	return s.perform("save settings", func() (Response, error) {
		settings, err := clientsetup.SaveSettings(input.GameDirectory, input.settings())
		if err != nil {
			return Response{}, err
		}
		if err := s.savePreferences(input.GameDirectory); err != nil {
			return Response{}, err
		}
		return success("Connection settings saved", settings), nil
	})
}

func (s *Studio) Patch(input Request) (Response, error) {
	return s.perform("patch client", func() (Response, error) {
		if _, err := clientsetup.SaveSettings(input.GameDirectory, input.settings()); err != nil {
			return Response{}, err
		}
		result, err := clientsetup.Patch(input.GameDirectory)
		if err != nil {
			return Response{}, err
		}
		if err := s.savePreferences(input.GameDirectory); err != nil {
			return Response{}, err
		}
		message := "Client entry point patched; the original backup was retained"
		if !result.Changed {
			message = "Client patch is already complete; no asset file was rewritten"
		}
		return success(message, result), nil
	})
}

func (s *Studio) Install(input Request) (Response, error) {
	return s.perform("install plugins", func() (Response, error) {
		if _, err := clientsetup.SaveSettings(input.GameDirectory, input.settings()); err != nil {
			return Response{}, err
		}
		plugins, err := s.plugins()
		if err != nil {
			return Response{}, err
		}
		result, err := clientsetup.InstallPlugins(input.GameDirectory, plugins)
		if err != nil {
			return Response{}, err
		}
		if err := s.savePreferences(input.GameDirectory); err != nil {
			return Response{}, err
		}
		message := "BD2 client plugins installed or updated"
		if !result.Changed {
			message = "BD2 client plugins are already up to date; no DLL was rewritten"
		}
		return success(message, result), nil
	})
}

func (s *Studio) Launch(input Request) (Response, error) {
	return s.perform("launch game", func() (Response, error) {
		if s.updates != nil {
			if err := s.updates.PrepareGame(s.context(), input.GameDirectory, input.ServerOrigin); err != nil {
				return Response{}, err
			}
		}
		versions := s.versions(input.GameDirectory)
		status, err := clientsetup.Inspect(input.GameDirectory, versions)
		if err != nil {
			return Response{}, err
		}
		if status.PatchedURL != clientsetup.PatchPlaceholder {
			return Response{}, errors.New("apply the client patch before launching the game")
		}
		if !status.BepInEx {
			return Response{}, errors.New("install BepInEx before launching the game")
		}
		policyCtx, cancel := context.WithTimeout(s.context(), 12*time.Second)
		_, err = clientsetup.FetchResourcePolicy(policyCtx, nil, input.settings(), versions)
		cancel()
		if err != nil {
			return Response{}, err
		}

		launch := func() (Response, error) {
			configured := input.settings()
			if s.updates != nil {
				runtime, err := s.updates.Runtime(input.GameDirectory)
				if err != nil {
					return Response{}, err
				}
				configured.Runtime = runtime
			}
			settings, err := clientsetup.SaveSettings(input.GameDirectory, configured)
			if err != nil {
				return Response{}, err
			}
			if settings.CDNMode == clientconfig.CDNLocal {
				if err := clientsetup.InstallLocalCatalog(input.GameDirectory, settings.LocalResourceDirectory, versions.BundleVersion); err != nil {
					return Response{}, err
				}
			}
			if err := s.savePreferences(input.GameDirectory); err != nil {
				return Response{}, err
			}
			installation, err := clientlayout.Resolve(status.GameDirectory)
			if err != nil {
				return Response{}, err
			}
			if !installation.SupportedOnHost() {
				return Response{}, fmt.Errorf("cannot launch a %s game client from this operating system", installation.Kind)
			}
			plugins, err := s.plugins()
			if err != nil {
				return Response{}, err
			}
			for _, artifact := range plugins {
				name := artifact.Name
				info, err := os.Stat(filepath.Join(installation.Plugins, name))
				if err != nil || !info.Mode().IsRegular() {
					return Response{}, fmt.Errorf("install or update the client plugins before launching; %s is missing", name)
				}
			}
			if err := s.launch(installation.LaunchTarget(), settings.ProxyURL, s.log()); err != nil {
				if errors.Is(err, errGameAlreadyRunning) {
					return success("Brown Dust II is already running", nil), nil
				}
				return Response{}, fmt.Errorf("launch Brown Dust II: %w", err)
			}
			s.log().Info("game launch requested", "platform", installation.Kind, "client_version", s.options.Versions.ClientVersion, "game_version", status.ClientVersion)
			return success("Brown Dust II started", nil), nil
		}
		if s.updates == nil {
			return launch()
		}
		var response Response
		err = s.updates.WithLaunch(s.context(), nil, status.GameDirectory, input.ServerOrigin, func() error {
			var err error
			response, err = launch()
			return err
		})
		return response, err
	})
}

// UpdateStatus reports launcher and plugin updates for the status panel.
func (s *Studio) UpdateStatus() clientupdate.Status {
	if s.updates == nil {
		return clientupdate.Status{}
	}
	return s.updates.Status()
}

// CheckUpdates checks now, downloads what is newer and installs a ready
// plugin set into closed game instances.
func (s *Studio) CheckUpdates() (Response, error) {
	if s.updates == nil {
		return Response{}, errors.New("update service is unavailable")
	}
	ctx := s.context()
	if err := s.updates.WaitIdle(ctx); err != nil {
		return Response{}, err
	}
	if err := s.updates.Check(ctx); err != nil {
		return Response{}, err
	}
	if err := s.updates.Download(ctx); err != nil {
		return Response{}, err
	}
	if err := s.updates.CommitPlugins(); err != nil {
		return Response{}, err
	}
	if err := s.updates.CommitGame(ctx); err != nil {
		return Response{}, err
	}
	return success("Update check completed", s.updates.Status()), nil
}

func (s *Studio) versions(game string) versionconfig.Config {
	if s.updates != nil {
		return s.updates.Versions(game)
	}
	return s.options.Versions
}

// ApplyLauncherUpdate restarts into the downloaded launcher. A running game
// is not closed; launching is paused until the new launcher is up.
func (s *Studio) ApplyLauncherUpdate() (Response, error) {
	if s.updates == nil {
		return Response{}, errors.New("update service is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.updates.ApplyLauncher(func() {
		if s.host.Quit != nil {
			go s.host.Quit(s.context())
		}
	})
	if err != nil {
		return Response{}, err
	}
	return success("Launcher is restarting to finish the update", nil), nil
}

func (s *Studio) Quit() {
	s.quitOnce.Do(func() {
		s.log().Info("client exit requested")
		if s.host.Quit != nil {
			s.host.Quit(s.context())
		}
	})
}

func (s *Studio) plugins() (clientplugin.Set, error) {
	if s.options.Development {
		if _, err := s.options.Plugins.Sources(); err != nil {
			return nil, err
		}
		return s.options.Plugins, nil
	}
	directory, err := s.packageDirectory()
	if err != nil {
		return nil, err
	}
	return clientplugin.Packaged(directory)
}
