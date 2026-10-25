// Package versionconfig loads repository-wide game compatibility, component
// release, seed, and resource versions for the server, client, and build tools.
package versionconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	commonconfig "bd2server/internal/common/config"
)

const FileName = "versions.json"

var (
	semanticVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	releaseVersion  = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+)\+(client|server)\.([0-9]+\.[0-9]+\.[0-9]+)$`)
	resourceVersion = regexp.MustCompile(`^[0-9]{14}$`)
	currentMu       sync.RWMutex
	current         *Config
)

// Config is the version contract shared by the server, client, and plugins.
// SourcePath is populated by Load and is not part of the JSON document.
type Config struct {
	GameVersion     string         `json:"game_version"`
	ClientVersion   string         `json:"client_version"`
	ServerVersion   string         `json:"server_version"`
	GameDataVersion string         `json:"game_data_version"`
	BundleVersion   string         `json:"bundle_version"`
	SeedDirectory   string         `json:"seed_directory"`
	Plugins         PluginVersions `json:"plugins"`
	// MinimumPlugins is the oldest plugin build this server still works
	// with; raise an entry only when a server change breaks older plugins.
	MinimumPlugins PluginVersions `json:"minimum_plugins"`
	SourcePath     string         `json:"-"`
}

type PluginVersions struct {
	LocalIdentity      string `json:"local_identity"`
	CaptureEnvironment string `json:"capture_environment"`
	LoginUI            string `json:"login_ui"`
	CashShop           string `json:"cash_shop"`
	FChat              string `json:"fchat"`
	FBattle            string `json:"fbattle,omitempty"`
}

// Load reads one explicit version file. Unknown fields and trailing JSON are
// rejected so a misspelled version key cannot silently select stale data.
func Load(path string) (Config, error) {
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return Config{}, fmt.Errorf("versionconfig: resolve path: %w", err)
	}
	cfg, err := commonconfig.Load(path, Config{}, commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true})
	if err != nil {
		return Config{}, fmt.Errorf("versionconfig: load %s: %w", path, err)
	}
	cfg.SourcePath = path
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("versionconfig: %s: %w", path, err)
	}
	return cfg, nil
}

func Game() string     { return Current().GameVersion }
func Server() string   { return Current().ServerVersion }
func State() string    { return Current().GameVersion }
func GameData() string { return Current().GameDataVersion }
func Bundle() string   { return Current().BundleVersion }

func (c Config) Validate() error {
	for name, value := range map[string]string{
		"game_version":                c.GameVersion,
		"plugins.local_identity":      c.Plugins.LocalIdentity,
		"plugins.capture_environment": c.Plugins.CaptureEnvironment,
		"plugins.login_ui":            c.Plugins.LoginUI,
		"plugins.fchat":               c.Plugins.FChat,
	} {
		if !semanticVersion.MatchString(value) {
			return fmt.Errorf("%s must be a numeric three-part version", name)
		}
	}
	for id, pair := range c.Plugins.pairs(c.MinimumPlugins) {
		if !semanticVersion.MatchString(pair[1]) || compareSemantic(pair[1], pair[0]) > 0 {
			return fmt.Errorf("minimum_plugins.%s must be a numeric three-part version no newer than plugins.%s", id, id)
		}
	}
	if c.Plugins.FBattle != "" && !semanticVersion.MatchString(c.Plugins.FBattle) {
		return errors.New("plugins.fbattle must be a numeric three-part version")
	}
	if err := validateReleaseVersion("client_version", c.ClientVersion, c.GameVersion, "client"); err != nil {
		return err
	}
	if err := validateReleaseVersion("server_version", c.ServerVersion, c.GameVersion, "server"); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"game_data_version": c.GameDataVersion, "bundle_version": c.BundleVersion,
	} {
		if !resourceVersion.MatchString(value) {
			return fmt.Errorf("%s must be a 14-digit version", name)
		}
	}
	if c.SeedDirectory == "" || filepath.IsAbs(c.SeedDirectory) {
		return errors.New("seed_directory must be a non-empty relative path")
	}
	clean := filepath.Clean(filepath.FromSlash(c.SeedDirectory))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("seed_directory must stay below the version file")
	}
	return nil
}

// ByID lists the plugin versions under their versions.json keys.
func (p PluginVersions) ByID() map[string]string {
	versions := map[string]string{"local_identity": p.LocalIdentity, "capture_environment": p.CaptureEnvironment, "login_ui": p.LoginUI, "cash_shop": p.CashShop, "fchat": p.FChat}
	if p.FBattle != "" {
		versions["fbattle"] = p.FBattle
	}
	return versions
}

func (p PluginVersions) pairs(minimum PluginVersions) map[string][2]string {
	current, lowest := p.ByID(), minimum.ByID()
	out := map[string][2]string{}
	for id, version := range current {
		out[id] = [2]string{version, lowest[id]}
	}
	return out
}

func compareSemantic(a, b string) int {
	var x, y [3]int
	_, _ = fmt.Sscanf(a, "%d.%d.%d", &x[0], &x[1], &x[2])
	_, _ = fmt.Sscanf(b, "%d.%d.%d", &y[0], &y[1], &y[2])
	for i := range x {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func validateReleaseVersion(name, value, game, component string) error {
	match := releaseVersion.FindStringSubmatch(value)
	if len(match) != 4 || match[1] != game || match[2] != component {
		return fmt.Errorf("%s must be %s+%s.X.Y.Z", name, game, component)
	}
	return nil
}

// Resolve interprets a configured repository asset relative to versions.json,
// rather than relative to the process working directory.
func (c Config) Resolve(relative string) string {
	return filepath.Join(filepath.Dir(c.SourcePath), filepath.FromSlash(relative))
}

// Find walks upward from the executable and working directory. Release packages put versions.json
// beside the executable; development commands find the repository root.
func Find() (Config, error) {
	var starts []string
	if executable, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(executable))
	}
	if working, err := os.Getwd(); err == nil {
		starts = append(starts, working)
	}
	seen := map[string]bool{}
	for _, start := range starts {
		for directory := filepath.Clean(start); ; directory = filepath.Dir(directory) {
			candidate := filepath.Join(directory, FileName)
			key := strings.ToLower(candidate)
			if !seen[key] {
				seen[key] = true
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return Load(candidate)
				}
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
		}
	}
	return Config{}, fmt.Errorf("versionconfig: %s not found; pass --version-config", FileName)
}

// Use selects an explicitly loaded configuration for all server packages.
func Use(cfg Config) {
	currentMu.Lock()
	defer currentMu.Unlock()
	copy := cfg
	current = &copy
}

// Current returns the process-wide selection, locating versions.json lazily in
// tests and development tools. Configuration failures are fatal programming or
// packaging errors and therefore panic before any state mutation can occur.
func Current() Config {
	currentMu.RLock()
	if current != nil {
		cfg := *current
		currentMu.RUnlock()
		return cfg
	}
	currentMu.RUnlock()
	cfg, err := Find()
	if err != nil {
		panic(err)
	}
	Use(cfg)
	return cfg
}
