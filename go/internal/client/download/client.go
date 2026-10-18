package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/release"
	"bd2server/internal/common/versionconfig"
)

type Options struct {
	PackageDir, Manifest               string
	GameDir, ResourceDir, ServerOrigin string
	Workers                            int
	ProbeOnly                          bool
	Pinned                             *release.GameResources
}

type Installation struct {
	GameDir, ResourceDir, ServerOrigin string
	Versions                           versionconfig.Config
}

func Client(ctx context.Context, options Options, out io.Writer) (Installation, error) {
	var result Installation
	if options.Workers < 1 || options.Workers > 16 {
		return result, errors.New("download workers must be 1-16")
	}
	packageDir, err := filepath.Abs(options.PackageDir)
	if err != nil {
		return result, err
	}
	manifestPath := options.Manifest
	if manifestPath == "" {
		manifestPath = filepath.Join(packageDir, "client-download.json")
	}
	var m manifest
	if options.Pinned != nil {
		m = *options.Pinned
		if err := m.Validate(); err != nil {
			return result, err
		}
		if len(m.ResourceFiles) == 0 {
			return result, errors.New("signed game update requires a complete resource inventory")
		}
	} else {
		m, err = commonconfig.Load(manifestPath, manifest{}, commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true, AllowBOM: true, RejectNull: true})
		if err != nil {
			return result, fmt.Errorf("download manifest: %w", err)
		}
	}
	if !timestamp.MatchString(m.GameDataVersion) || !timestamp.MatchString(m.BundleVersion) || !gameVersionPattern.MatchString(m.GameVersion) || m.Platform != "StandaloneWindows64" || !strings.HasPrefix(m.GamePath, "/Build/") {
		return result, errors.New("invalid Windows game download manifest")
	}
	if !sha256Pattern.MatchString(m.GameSHA256) || !sha256Pattern.MatchString(m.DataSHA256) || !sha256Pattern.MatchString(m.CatalogSHA256) || m.GameBytes <= 0 || m.DataBytes <= 0 || m.CatalogBytes <= 0 {
		return result, errors.New("download manifest requires pinned checksums and positive lengths")
	}
	result.Versions = versionconfig.Config{GameVersion: m.GameVersion, GameDataVersion: m.GameDataVersion, BundleVersion: m.BundleVersion}
	if options.GameDir == "" {
		options.GameDir = filepath.Join(packageDir, "game")
	}
	if options.ResourceDir == "" {
		options.ResourceDir = filepath.Join(packageDir, "resources")
	}
	result.GameDir, err = filepath.Abs(options.GameDir)
	if err != nil {
		return result, err
	}
	result.ResourceDir, err = filepath.Abs(options.ResourceDir)
	if err != nil {
		return result, err
	}
	result.ServerOrigin = options.ServerOrigin
	if result.ServerOrigin == "" {
		result.ServerOrigin = m.ServerOrigin
	}
	if !options.ProbeOnly {
		if err := requireStopped(result.GameDir); err != nil {
			return result, err
		}
	}
	d, err := newDownloader(packageDir, out)
	if err != nil {
		return result, err
	}
	defer d.client.CloseIdleConnections()
	dataPath := "/GameData/" + m.GameDataVersion
	serverPath := "/ServerData/" + m.Platform + "/HD/" + m.BundleVersion
	if options.ProbeOnly {
		for _, probe := range []struct {
			origins     []string
			path, label string
		}{{m.BuildOrigins, m.GamePath, "Game ZIP"}, {m.ResourceOrigins, dataPath + "/release/common-dbdata.bin", "GameData"}, {m.ResourceOrigins, serverPath + "/catalog_alpha.json", "ServerData"}} {
			if _, err := d.fastest(ctx, probe.origins, probe.path, probe.label); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	d.measureOrigins = true
	archive := filepath.Join(packageDir, "downloads", "BrownDust-II-"+m.GameVersion+".zip")
	gameFile := file{remote: m.GamePath, target: archive, digest: m.GameSHA256, size: m.GameBytes}
	builds := m.BuildOrigins
	if !d.verified(gameFile) {
		builds, err = d.fastest(ctx, m.BuildOrigins, m.GamePath, "Game ZIP")
		if err != nil {
			return result, err
		}
	}
	if err := d.receiveAll(ctx, []file{gameFile}, builds, 1); err != nil {
		return result, err
	}
	if err := extractArchive(archive, result.GameDir, true, m.GameSHA256); err != nil {
		return result, err
	}
	if err := d.resources(ctx, result.ResourceDir, m, serverPath, dataPath, m.ResourceOrigins, m.ResourceOrigins, options.Workers); err != nil {
		return result, err
	}
	d.log("Downloaded: game=%s resources=%s", result.GameDir, result.ResourceDir)
	return result, nil
}
