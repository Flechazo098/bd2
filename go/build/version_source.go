package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type versionSourceConfig struct {
	GameVersion     string `json:"game_version"`
	ClientVersion   string `json:"client_version"`
	ServerVersion   string `json:"server_version"`
	GameDataVersion string `json:"game_data_version"`
	BundleVersion   string `json:"bundle_version"`
	SeedDirectory   string `json:"seed_directory"`
	Plugins         struct {
		LocalIdentity      string `json:"local_identity"`
		CaptureEnvironment string `json:"capture_environment"`
		LoginUI            string `json:"login_ui"`
		CashShop           string `json:"cash_shop"`
	} `json:"plugins"`
}

func (t task) generateVersionSource(args []string) error {
	options := flag.NewFlagSet("version-source", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	config := options.String("config", "", "version configuration")
	output := options.String("output", "", "generated C# source")
	plugin := options.String("plugin", "", "plugin version key")
	if err := options.Parse(args); err != nil {
		return err
	}
	if options.NArg() != 0 || *config == "" || *output == "" || *plugin == "" {
		return errors.New("version-source requires --config, --output and --plugin, with no positional arguments")
	}
	var versions versionSourceConfig
	if err := readJSON(*config, &versions, true); err != nil {
		return err
	}
	pluginVersion, err := versions.validate(*plugin)
	if err != nil {
		return fmt.Errorf("%s: %w", *config, err)
	}
	configPath, err := filepath.Abs(*config)
	if err != nil {
		return err
	}
	outputPath, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if outputPath == configPath {
		return errors.New("version-source output must not replace the version configuration")
	}
	configInfo, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	if outputInfo, statErr := os.Stat(outputPath); statErr == nil && os.SameFile(configInfo, outputInfo) {
		return errors.New("version-source output must not replace the version configuration")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	source := fmt.Sprintf(`// Generated from versions.json. Do not edit.
namespace Bd2Build
{
    internal static class Versions
    {
        internal const string Game = "%s";
        internal const string ClientRelease = "%s";
        internal const string GameData = "%s";
        internal const string Bundle = "%s";
        internal const string Plugin = "%s";
    }
}
`, versions.GameVersion, versions.ClientVersion, versions.GameDataVersion, versions.BundleVersion, pluginVersion)
	return writeVersionSource(outputPath, []byte(source))
}

func (c versionSourceConfig) validate(plugin string) (string, error) {
	plugins := map[string]string{"local_identity": c.Plugins.LocalIdentity, "capture_environment": c.Plugins.CaptureEnvironment, "login_ui": c.Plugins.LoginUI, "cash_shop": c.Plugins.CashShop}
	selected, found := plugins[plugin]
	if !found {
		return "", fmt.Errorf("unsupported plugin %q", plugin)
	}
	semver := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	resource := regexp.MustCompile(`^[0-9]{14}$`)
	if !semver.MatchString(c.GameVersion) {
		return "", errors.New("game_version must be a numeric three-part version")
	}
	for _, key := range []string{"local_identity", "capture_environment", "login_ui", "cash_shop"} {
		if !semver.MatchString(plugins[key]) {
			return "", fmt.Errorf("plugins.%s must be a numeric three-part version", key)
		}
	}
	for _, release := range []struct{ name, value, component string }{{"client_version", c.ClientVersion, "client"}, {"server_version", c.ServerVersion, "server"}} {
		prefix := c.GameVersion + "+" + release.component + "."
		if suffix, found := strings.CutPrefix(release.value, prefix); !found || !semver.MatchString(suffix) {
			return "", fmt.Errorf("%s must be %sX.Y.Z", release.name, prefix)
		}
	}
	for _, version := range []struct{ name, value string }{{"game_data_version", c.GameDataVersion}, {"bundle_version", c.BundleVersion}} {
		if !resource.MatchString(version.value) {
			return "", fmt.Errorf("%s must be a 14-digit version", version.name)
		}
	}
	seed := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(c.SeedDirectory, `\`, "/")))
	if c.SeedDirectory == "" || filepath.IsAbs(seed) || strings.Contains(c.SeedDirectory, ":") || seed == ".." || strings.HasPrefix(seed, ".."+string(filepath.Separator)) {
		return "", errors.New("seed_directory must be a non-empty relative path below the version file")
	}
	return selected, nil
}

func writeVersionSource(path string, source []byte) (err error) {
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, source) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".version-source-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() {
		if removeErr := os.Remove(temporary); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	_, writeErr := file.Write(source)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return os.Rename(temporary, path)
}
