package preparation

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	clientconfig "bd2server/internal/client/config"
	"bd2server/internal/client/download"
	"bd2server/internal/client/layout"
	"bd2server/internal/client/setup"
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/taskgraph"
	"bd2server/internal/common/versionconfig"
)

type Options struct {
	PackageDirectory, DownloadManifest, VersionConfig string
	GameDirectory, ResourceDirectory                  string
	Settings                                          clientconfig.Settings
	Versions                                          versionconfig.Config
	Workers                                           int
	DownloadOnly, BaseOnly, ProbeOnly, PlanOnly       bool
	DownloadIfMissing                                 bool
	LocalIdentityPlugin, LoginUIPlugin                string
	CashShopPlugin, FChatPlugin                       string
	PrepareGame                                       func(context.Context, string, clientconfig.Settings) (versionconfig.Config, error)
	SavePreferences                                   func(string) error
}

type Result struct {
	GameDirectory, ResourceDirectory string
	Settings                         clientconfig.Settings
	Versions                         versionconfig.Config
	Status                           setup.GameStatus
	Plan                             []string
}

func Run(ctx context.Context, options Options, out io.Writer) (Result, error) {
	var result Result
	if options.Workers < 1 || options.Workers > 16 {
		return result, errors.New("prepare workers must be 1-16")
	}
	if options.DownloadOnly && options.BaseOnly {
		return result, errors.New("--download and --base select separate stages; omit both to run all stages")
	}
	if options.BaseOnly && options.ProbeOnly {
		return result, errors.New("--probe-only cannot be used with --base")
	}
	if options.PackageDirectory == "" {
		executable, err := os.Executable()
		if err != nil {
			return result, err
		}
		options.PackageDirectory = filepath.Dir(executable)
	}
	directory, err := filepath.Abs(options.PackageDirectory)
	if err != nil {
		return result, err
	}
	if out == nil {
		out = io.Discard
	}
	result.GameDirectory, result.ResourceDirectory = options.GameDirectory, options.ResourceDirectory
	if result.GameDirectory == "" {
		result.GameDirectory = filepath.Join(directory, "game")
	}
	if result.ResourceDirectory == "" {
		result.ResourceDirectory = options.Settings.LocalResourceDirectory
	}
	if result.ResourceDirectory == "" {
		result.ResourceDirectory = filepath.Join(directory, "resources")
	}
	result.Settings, result.Versions = options.Settings, options.Versions
	var downloaded versionconfig.Config
	didDownload := false
	graph, err := taskgraph.New(
		taskgraph.Task{Name: "download", Run: func(ctx context.Context) error {
			if options.DownloadIfMissing && !options.ProbeOnly {
				if installation, err := layout.Resolve(result.GameDirectory); err == nil &&
					(result.Settings.CDNMode != clientconfig.CDNLocal || installation.Kind == layout.MacOS) {
					result.GameDirectory = installation.Root
					return nil
				}
				if runtime.GOOS != "windows" {
					return errors.New("automatic game installation is available only on Windows; select an installed game")
				}
			}
			installed, err := download.Client(ctx, download.Options{
				PackageDir: directory, Manifest: options.DownloadManifest,
				GameDir: result.GameDirectory, ResourceDir: result.ResourceDirectory, ServerOrigin: result.Settings.ServerOrigin,
				Workers: options.Workers, ProbeOnly: options.ProbeOnly,
			}, out)
			if err != nil {
				return err
			}
			result.GameDirectory, result.ResourceDirectory = installed.GameDir, installed.ResourceDir
			result.Settings.ServerOrigin = installed.ServerOrigin
			downloaded, didDownload = installed.Versions, true
			if options.DownloadOnly || options.ProbeOnly {
				result.Versions = installed.Versions
			}
			return nil
		}},
		taskgraph.Task{Name: "base:configuration", Run: func(context.Context) error {
			if result.Settings.ServerOrigin == "" {
				manifest := options.DownloadManifest
				if manifest == "" {
					manifest = filepath.Join(directory, "client-download.json")
				}
				defaults, err := commonconfig.Load(manifest, struct {
					ServerOrigin string `json:"server_origin"`
				}{}, commonconfig.DecodeOptions{AllowBOM: true, RejectTrailing: true})
				if err != nil {
					return err
				}
				result.Settings.ServerOrigin = defaults.ServerOrigin
			}
			if result.Versions.GameVersion == "" {
				path := options.VersionConfig
				if path == "" {
					path = filepath.Join(directory, versionconfig.FileName)
				}
				var err error
				result.Versions, err = versionconfig.Load(path)
				if err != nil {
					return err
				}
			}
			if didDownload && (result.Versions.GameVersion != downloaded.GameVersion || result.Versions.GameDataVersion != downloaded.GameDataVersion || result.Versions.BundleVersion != downloaded.BundleVersion) {
				return errors.New("client package and download manifest versions differ")
			}
			return nil
		}},
		taskgraph.Task{Name: "base:settings", DependsOn: []string{"base:configuration"}, Run: func(context.Context) error {
			if result.Settings.CDNMode == clientconfig.CDNLocal && result.Settings.LocalResourceDirectory == "" {
				result.Settings.LocalResourceDirectory = result.ResourceDirectory
			}
			var err error
			result.Settings, err = clientconfig.Normalize(result.Settings)
			return err
		}},
		taskgraph.Task{Name: "base:update", DependsOn: []string{"base:settings"}, Run: func(ctx context.Context) error {
			if options.PrepareGame == nil {
				return nil
			}
			if saved, err := clientconfig.Load(result.GameDirectory); err == nil {
				result.Settings.Runtime = saved.Runtime
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if _, err := clientconfig.Save(result.GameDirectory, result.Settings); err != nil {
				return err
			}
			versions, err := options.PrepareGame(ctx, result.GameDirectory, result.Settings)
			if err != nil {
				return err
			}
			result.Versions = versions
			saved, err := clientconfig.Load(result.GameDirectory)
			if err != nil {
				return err
			}
			result.Settings.Runtime = saved.Runtime
			return nil
		}},
		taskgraph.Task{Name: "base:resources", DependsOn: []string{"base:update"}, Run: func(ctx context.Context) error {
			policyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := setup.FetchResourcePolicy(policyContext, nil, result.Settings, result.Versions)
			return err
		}},
		taskgraph.Task{Name: "base:bepinex", DependsOn: []string{"base:resources"}, Run: func(context.Context) error {
			installation, err := layout.Resolve(result.GameDirectory)
			if err != nil {
				return err
			}
			complete := regularFile(filepath.Join(installation.BepInEx, "core", "BepInEx.dll"))
			if installation.Kind == layout.Windows {
				complete = complete && regularFile(filepath.Join(installation.Root, "winhttp.dll")) && regularFile(filepath.Join(installation.Root, "doorstop_config.ini"))
			}
			if complete {
				return nil
			}
			if installation.Kind != layout.Windows {
				return errors.New("install BepInEx for the selected macOS game before preparing the client")
			}
			return download.InstallBepInEx(directory, installation.Root)
		}},
		taskgraph.Task{Name: "base:patch", DependsOn: []string{"base:bepinex"}, Run: func(context.Context) error {
			if _, err := setup.SaveSettings(result.GameDirectory, result.Settings); err != nil {
				return err
			}
			if result.Settings.CDNMode == clientconfig.CDNLocal {
				if err := setup.InstallLocalCatalog(result.GameDirectory, result.Settings.LocalResourceDirectory, result.Versions.BundleVersion); err != nil {
					return err
				}
			}
			_, err := setup.Patch(result.GameDirectory)
			return err
		}},
		taskgraph.Task{Name: "base:plugins", DependsOn: []string{"base:patch"}, Run: func(context.Context) error {
			sources := []*string{&options.LocalIdentityPlugin, &options.LoginUIPlugin, &options.CashShopPlugin, &options.FChatPlugin}
			for n, name := range []string{"BD2LocalIdentity.dll", "BD2LoginUI.dll", "BD2CashShop.dll", "BD2FChat.dll"} {
				if *sources[n] == "" {
					*sources[n] = filepath.Join(directory, "plugins", name)
				}
			}
			_, err := setup.InstallPlugins(result.GameDirectory, options.LocalIdentityPlugin, options.LoginUIPlugin, options.CashShopPlugin, options.FChatPlugin)
			return err
		}},
		taskgraph.Task{Name: "base:preferences", DependsOn: []string{"base:plugins"}, Run: func(context.Context) error {
			save := options.SavePreferences
			if save == nil {
				save = clientconfig.SavePreferences
			}
			return save(result.GameDirectory)
		}},
		taskgraph.Task{Name: "base", DependsOn: []string{"base:preferences"}, Run: func(context.Context) error {
			var err error
			result.Status, err = setup.Inspect(result.GameDirectory, result.Versions)
			return err
		}},
		taskgraph.Task{Name: "prepare", DependsOn: []string{"download", "base"}},
	)
	if err != nil {
		return result, err
	}
	target := "prepare"
	if options.DownloadOnly || options.ProbeOnly {
		target = "download"
	} else if options.BaseOnly {
		target = "base"
	}
	result.Plan, err = graph.Plan(target)
	if err != nil || options.PlanOnly {
		return result, err
	}
	err = graph.Run(ctx, target)
	return result, err
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
