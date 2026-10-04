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
	clientsetup "bd2server/internal/client/setup"
)

//go:embed web/index.html
var webFS embed.FS

var errGameAlreadyRunning = errors.New("Brown Dust II is already running")

type Options struct {
	InitialGameDir      string
	Logger              *slog.Logger
	LogPath             string
	Versions            clientconfig.ReleaseVersions
	LocalIdentityPlugin string
	LoginUIPlugin       string
}

type Request struct {
	ProxyURL               string               `json:"proxy_url"`
	GameDirectory          string               `json:"game_directory"`
	ServerOrigin           string               `json:"server_origin"`
	CDNMode                clientconfig.CDNMode `json:"cdn_mode"`
	LocalResourceDirectory string               `json:"local_resource_directory"`
	UILanguage             string               `json:"ui_language"`
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
	launch          func(string, string) error
	savePreferences func(string) error
}

func NewStudio(options Options, host NativeHost) *Studio {
	return &Studio{options: options, host: host, ctx: context.Background(), launch: launchGame, savePreferences: clientconfig.SavePreferences}
}

func (s *Studio) Startup(ctx context.Context) {
	owned, cancel := context.WithCancel(ctx)
	s.ctxMu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.ctx, s.cancel = owned, cancel
	s.ctxMu.Unlock()
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
	if state.GameDirectory == "" {
		return state
	}
	if _, err := clientsetup.Inspect(state.GameDirectory, s.options.Versions); err != nil {
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

func (s *Studio) chooseDirectory(language, kind string) (string, error) {
	if s.host.BrowseDirectory == nil {
		return "", errors.New("native directory picker is unavailable")
	}
	prompt := "Select the Brown Dust II installation directory"
	if kind == "resources" {
		prompt = "Select the CDN directory containing ServerData and GameData"
	}
	if language == "zh-CN" {
		prompt = "选择 Brown Dust II 安装目录"
		if kind == "resources" {
			prompt = "选择包含 ServerData 和 GameData 的 CDN 目录"
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
		dir, err := s.chooseDirectory(input.UILanguage, "game")
		if err != nil {
			return Response{}, err
		}
		if dir == "" {
			return success("Selection cancelled", nil), nil
		}
		status, err := clientsetup.Inspect(dir, s.options.Versions)
		return success("Game client found", status), err
	})
}

func (s *Studio) BrowseResources(input Request) (Response, error) {
	return s.perform("browse resource directory", func() (Response, error) {
		dir, err := s.chooseDirectory(input.UILanguage, "resources")
		if err != nil {
			return Response{}, err
		}
		if dir == "" {
			return success("Selection cancelled", nil), nil
		}
		policy, err := clientsetup.FetchResourcePolicy(s.context(), nil, clientconfig.Settings{ServerOrigin: "http://127.0.0.1", CDNMode: clientconfig.CDNLocal, LocalResourceDirectory: dir}, s.options.Versions)
		return success("Local resource directory found", policy), err
	})
}

func (s *Studio) Inspect(input Request) (Response, error) {
	return s.perform("inspect game directory", func() (Response, error) {
		status, err := clientsetup.Inspect(input.GameDirectory, s.options.Versions)
		return success("Game directory is valid", status), err
	})
}

func (s *Studio) Resources(input Request) (Response, error) {
	return s.perform("check resource policy", func() (Response, error) {
		ctx, cancel := context.WithTimeout(s.context(), 12*time.Second)
		defer cancel()
		policy, err := clientsetup.FetchResourcePolicy(ctx, nil, input.settings(), s.options.Versions)
		message := "The client will use the release-locked official CDN"
		if policy.Mode == clientconfig.CDNLocal {
			message = "Local resources verified"
		} else if policy.Mode == clientconfig.CDNServer {
			message = "Server resource policy verified"
		}
		return success(message, policy), err
	})
}

func (s *Studio) Save(input Request) (Response, error) {
	return s.perform("save settings", func() (Response, error) {
		settings, err := clientsetup.SaveSettings(input.GameDirectory, input.settings(), s.options.Versions)
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
		result, err := clientsetup.Patch(input.GameDirectory, input.settings(), s.options.Versions)
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
		result, err := clientsetup.InstallPlugins(input.GameDirectory, input.settings(), s.options.Versions, s.options.LocalIdentityPlugin, s.options.LoginUIPlugin)
		if err != nil {
			return Response{}, err
		}
		if err := s.savePreferences(input.GameDirectory); err != nil {
			return Response{}, err
		}
		message := "BD2 client plugins installed or updated"
		if !result.LocalIdentity.Changed && !result.LoginUI.Changed {
			message = "BD2 client plugins are already up to date; no DLL was rewritten"
		}
		return success(message, result), nil
	})
}

func (s *Studio) Launch(input Request) (Response, error) {
	return s.perform("launch game", func() (Response, error) {
		status, err := clientsetup.Inspect(input.GameDirectory, s.options.Versions)
		if err != nil {
			return Response{}, err
		}
		if status.PatchedURL != clientsetup.PatchPlaceholder {
			return Response{}, errors.New("apply the client patch before launching the game")
		}
		if !status.BepInEx {
			return Response{}, errors.New("install BepInEx before launching the game")
		}
		ctx, cancel := context.WithTimeout(s.context(), 12*time.Second)
		defer cancel()
		if _, err := clientsetup.FetchResourcePolicy(ctx, nil, input.settings(), s.options.Versions); err != nil {
			return Response{}, err
		}
		settings, err := clientsetup.SaveSettings(input.GameDirectory, input.settings(), s.options.Versions)
		if err != nil {
			return Response{}, err
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
		for _, name := range []string{"BD2LocalIdentity.dll", "BD2LoginUI.dll"} {
			info, err := os.Stat(filepath.Join(installation.Plugins, name))
			if err != nil || !info.Mode().IsRegular() {
				return Response{}, fmt.Errorf("install or update the client plugins before launching; %s is missing", name)
			}
		}
		if err := s.launch(installation.LaunchTarget(), settings.ProxyURL); err != nil {
			if errors.Is(err, errGameAlreadyRunning) {
				return success("Brown Dust II is already running", nil), nil
			}
			return Response{}, fmt.Errorf("launch Brown Dust II: %w", err)
		}
		s.log().Info("game launch requested", "platform", installation.Kind, "client_version", s.options.Versions.ClientVersion, "game_version", status.ClientVersion)
		return success("Brown Dust II started", nil), nil
	})
}

func (s *Studio) Quit() {
	s.quitOnce.Do(func() {
		s.log().Info("client exit requested")
		if s.host.Quit != nil {
			s.host.Quit(s.context())
		}
	})
}
