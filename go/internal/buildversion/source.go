package buildversion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	GameVersion     string         `json:"game_version"`
	ClientVersion   string         `json:"client_version"`
	ServerVersion   string         `json:"server_version"`
	GameDataVersion string         `json:"game_data_version"`
	BundleVersion   string         `json:"bundle_version"`
	SeedDirectory   string         `json:"seed_directory"`
	Plugins         pluginVersions `json:"plugins"`
	MinimumPlugins  pluginVersions `json:"minimum_plugins"`
}

type pluginVersions struct {
	LocalIdentity      string `json:"local_identity"`
	CaptureEnvironment string `json:"capture_environment"`
	LoginUI            string `json:"login_ui"`
	CashShop           string `json:"cash_shop"`
	FChat              string `json:"fchat"`
	FBattle            string `json:"fbattle,omitempty"`
}

func readConfig(path string, config *Config) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(config); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("version configuration must contain exactly one JSON object")
	}
	return nil
}

func GameVersion(path string) (string, error) {
	var config Config
	if err := readConfig(path, &config); err != nil {
		return "", err
	}
	if _, err := config.validate("local_identity"); err != nil {
		return "", err
	}
	return config.GameVersion, nil
}

func Generate(config, output, plugin string) error {
	var versions Config
	if err := readConfig(config, &versions); err != nil {
		return err
	}
	pluginVersion, err := versions.validate(plugin)
	if err != nil {
		return fmt.Errorf("%s: %w", config, err)
	}
	configPath, err := filepath.Abs(config)
	if err != nil {
		return err
	}
	outputPath, err := filepath.Abs(output)
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

func (c Config) validate(plugin string) (string, error) {
	plugins := map[string]string{"local_identity": c.Plugins.LocalIdentity, "capture_environment": c.Plugins.CaptureEnvironment, "login_ui": c.Plugins.LoginUI, "cash_shop": c.Plugins.CashShop, "fchat": c.Plugins.FChat}
	if c.Plugins.FBattle != "" {
		plugins["fbattle"] = c.Plugins.FBattle
	}
	selected, found := plugins[plugin]
	if !found {
		return "", fmt.Errorf("unsupported plugin %q", plugin)
	}
	semver := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	resource := regexp.MustCompile(`^[0-9]{14}$`)
	if !semver.MatchString(c.GameVersion) {
		return "", errors.New("game_version must be a numeric three-part version")
	}
	for key, value := range plugins {
		if !semver.MatchString(value) {
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
