package app

import (
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/resources/fetch"
	"context"
	"errors"
	"flag"
	"log/slog"
)

func Resources(args []string) error {
	if len(args) == 0 || args[0] != "fetch" {
		return errors.New("resources requires the fetch subcommand")
	}
	fs := flag.NewFlagSet("resources fetch", flag.ContinueOnError)
	versionConfigPath := fs.String("version-config", "", "repository versions.json override")
	output := fs.String("output", "", "resource mirror output directory (required)")
	platform := fs.String("platform", "StandaloneWindows64", "official ServerData platform")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("resources fetch requires --output")
	}
	var versions versionconfig.Config
	var err error
	if *versionConfigPath == "" {
		versions, err = versionconfig.Find()
	} else {
		versions, err = versionconfig.Load(*versionConfigPath)
	}
	if err != nil {
		return err
	}
	manifest, err := resourcefetch.Fetch(context.Background(), resourcefetch.Options{
		OutputRoot: *output, Platform: *platform, BundleVersion: versions.BundleVersion,
		GameDataVersion: versions.GameDataVersion,
		Progress:        func(message string) { slog.Info(message) },
	})
	if err != nil {
		return err
	}
	slog.Info("official resource mirror complete", "output", *output, "bundles", manifest.ServerData.Bundles, "bytes", manifest.ServerData.Bytes)
	return nil
}
