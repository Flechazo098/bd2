// Package versionconfig loads the repository-wide client, protocol, and
// resource version selection. The same versions.json also drives plugin builds.
package versionconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const FileName = "versions.json"

var (
	semanticVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	resourceVersion = regexp.MustCompile(`^[0-9]{14}$`)
	currentMu       sync.RWMutex
	current         *Config
)

// Config is the single version selection shared by the server and plugins.
// SourcePath is populated by Load and is not part of the JSON document.
type Config struct {
	ClientVersion   string         `json:"client_version"`
	ProtocolVersion string         `json:"protocol_version"`
	GameDataVersion string         `json:"game_data_version"`
	BundleVersion   string         `json:"bundle_version"`
	SeedDirectory   string         `json:"seed_directory"`
	Plugins         PluginVersions `json:"plugins"`
	SourcePath      string         `json:"-"`
}

type PluginVersions struct {
	LocalIdentity      string `json:"local_identity"`
	CaptureEnvironment string `json:"capture_environment"`
}

// Load reads one explicit version file. Unknown fields and trailing JSON are
// rejected so a misspelled version key cannot silently select stale data.
func Load(path string) (Config, error) {
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return Config{}, fmt.Errorf("versionconfig: resolve path: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("versionconfig: read %s: %w", path, err)
	}
	var cfg Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("versionconfig: decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Config{}, fmt.Errorf("versionconfig: trailing JSON in %s", path)
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("versionconfig: trailing data in %s: %w", path, err)
	}
	cfg.SourcePath = path
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("versionconfig: %s: %w", path, err)
	}
	return cfg, nil
}

func Client() string   { return Current().ClientVersion }
func Protocol() string { return Current().ProtocolVersion }
func GameData() string { return Current().GameDataVersion }
func Bundle() string   { return Current().BundleVersion }

func (c Config) Validate() error {
	for name, value := range map[string]string{
		"client_version": c.ClientVersion, "protocol_version": c.ProtocolVersion,
		"plugins.local_identity":      c.Plugins.LocalIdentity,
		"plugins.capture_environment": c.Plugins.CaptureEnvironment,
	} {
		if !semanticVersion.MatchString(value) {
			return fmt.Errorf("%s must be a numeric three-part version", name)
		}
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

// Resolve interprets a configured repository asset relative to versions.json,
// rather than relative to the process working directory.
func (c Config) Resolve(relative string) string {
	return filepath.Join(filepath.Dir(c.SourcePath), filepath.FromSlash(relative))
}

// Find searches an explicit environment override first, then walks upward from
// the executable and working directory. Release packages put versions.json
// beside the executable; development commands find the repository root.
func Find() (Config, error) {
	if path := os.Getenv("BD2_VERSION_CONFIG"); path != "" {
		return Load(path)
	}
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
	return Config{}, fmt.Errorf("versionconfig: %s not found; pass --version-config or set BD2_VERSION_CONFIG", FileName)
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
