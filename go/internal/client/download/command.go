package download

import (
	"context"
	"errors"
	"flag"
	"io"

	"path/filepath"
	"regexp"
	"strings"

	"bd2server/internal/common/release"
)

type manifest = release.GameResources

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
