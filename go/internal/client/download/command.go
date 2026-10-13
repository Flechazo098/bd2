package download

import (
	"context"
	"errors"
	"flag"
	"io"

	"path/filepath"
	"regexp"
	"strings"
)

type manifest struct {
	GameVersion     string   `json:"game_version"`
	GameDataVersion string   `json:"game_data_version"`
	BundleVersion   string   `json:"bundle_version"`
	Platform        string   `json:"platform"`
	ServerOrigin    string   `json:"server_origin"`
	BuildOrigins    []string `json:"build_origins"`
	ResourceOrigins []string `json:"resource_origins"`
	GamePath        string   `json:"game_path"`
	GameSHA256      string   `json:"game_sha256"`
	GameBytes       int64    `json:"game_bytes"`
	DataSHA256      string   `json:"data_sha256"`
	DataBytes       int64    `json:"data_bytes"`
	CatalogSHA256   string   `json:"catalog_sha256"`
	CatalogBytes    int64    `json:"catalog_bytes"`
	CatalogHash     string   `json:"catalog_hash"`
}

var timestamp = regexp.MustCompile(`^[0-9]{14}$`)
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var gameVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func RunResources(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("download-resources", flag.ContinueOnError)
	fs.SetOutput(out)
	root := fs.String("output", "", "output directory containing ServerData and GameData")
	platform := fs.String("platform", "StandaloneWindows64", "resource platform")
	bundle := fs.String("bundle-version", "", "14-digit ServerData version")
	data := fs.String("game-data-version", "", "14-digit GameData version")
	serverURL := fs.String("server-data-url", "https://dl.bd2.pmang.cloud/ServerData", "ServerData root URL")
	dataURL := fs.String("game-data-url", "https://dl.bd2.pmang.cloud/GameData", "GameData root URL")
	workers := fs.Int("workers", 8, "parallel downloads (1-16)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *root == "" || !timestamp.MatchString(*bundle) || !timestamp.MatchString(*data) || *workers < 1 || *workers > 16 || strings.ContainsAny(*platform, "/\\") || *platform == "" {
		return errors.New("download-resources requires --output, --bundle-version, --game-data-version and workers 1-16")
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	d, err := newDownloader(abs, out)
	if err != nil {
		return err
	}
	defer d.client.CloseIdleConnections()
	m := manifest{BundleVersion: *bundle, GameDataVersion: *data, Platform: *platform}
	return d.resources(ctx, abs, m, "/"+*platform+"/HD/"+*bundle, "/"+*data, []string{strings.TrimRight(*serverURL, "/")}, []string{strings.TrimRight(*dataURL, "/")}, *workers)
}
