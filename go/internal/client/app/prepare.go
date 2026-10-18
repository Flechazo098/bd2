package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"

	clientconfig "bd2server/internal/client/config"
	"bd2server/internal/client/preparation"
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/versionconfig"
)

func (s *Studio) packageDirectory() (string, error) {
	if s.options.PackageDirectory != "" {
		return filepath.Abs(s.options.PackageDirectory)
	}
	executable, err := os.Executable()
	return filepath.Dir(executable), err
}

func (s *Studio) downloadManifest(directory string) string {
	if s.options.DownloadManifest != "" {
		return s.options.DownloadManifest
	}
	return filepath.Join(directory, "client-download.json")
}

func (s *Studio) installationDefaults(state *InitialState) {
	directory, err := s.packageDirectory()
	if err != nil {
		return
	}
	defaults, err := commonconfig.Load(s.downloadManifest(directory), struct {
		ServerOrigin string `json:"server_origin"`
	}{}, commonconfig.DecodeOptions{AllowBOM: true, RejectTrailing: true})
	if err == nil {
		if origin, err := clientconfig.NormalizeOrigin(defaults.ServerOrigin); err == nil {
			state.ServerOrigin = origin
		}
	}
	if runtime.GOOS == "windows" && state.GameDirectory == "" {
		state.GameDirectory = filepath.Join(directory, "game")
		state.CDNMode = clientconfig.CDNLocal
	}
	state.LocalResourceDirectory = filepath.Join(directory, "resources")
}

func (s *Studio) Prepare(input Request) (Response, error) {
	return s.perform("prepare client", func() (Response, error) {
		directory, err := s.packageDirectory()
		if err != nil {
			return Response{}, err
		}
		result, err := preparation.Run(s.context(), preparation.Options{
			PackageDirectory: directory, DownloadManifest: s.downloadManifest(directory),
			GameDirectory: input.GameDirectory, Settings: input.settings(), Versions: s.versions(input.GameDirectory),
			Workers: 8, DownloadIfMissing: true,
			LocalIdentityPlugin: s.options.LocalIdentityPlugin, LoginUIPlugin: s.options.LoginUIPlugin,
			CashShopPlugin: s.options.CashShopPlugin, FChatPlugin: s.options.FChatPlugin,
			SavePreferences: s.savePreferences,
			PrepareGame: func(ctx context.Context, game string, settings clientconfig.Settings) (versionconfig.Config, error) {
				if s.updates != nil {
					if err := s.updates.PrepareGame(ctx, game, settings.ServerOrigin); err != nil {
						return versionconfig.Config{}, err
					}
				}
				return s.versions(game), nil
			},
		}, io.Discard)
		if err != nil {
			return Response{}, err
		}
		return success("Client prepared", result.Status), nil
	})
}
