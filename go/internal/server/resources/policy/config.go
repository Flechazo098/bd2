// Package resourcepolicy loads the server-authoritative client resource policy.
package resourcepolicy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

const (
	FileName              = "resources.json"
	ModeOfficial          = "official"
	ModeServer            = "server"
	OfficialServerDataURL = "https://dl.bd2.pmang.cloud/ServerData"
	OfficialGameDataURL   = "https://dl.bd2.pmang.cloud/GameData"
)

// Config is private server configuration. It contains no credentials, but it
// remains server-authoritative so a client cannot silently select resources
// that disagree with the server operator's policy.
type Config struct {
	Mode          string `json:"mode"`
	ServerDataURL string `json:"server_data_url,omitempty"`
	GameDataURL   string `json:"game_data_url,omitempty"`
}

// Public is the complete resource policy returned to a client. Versions come
// from the server's validated versions.json rather than client input.
type Public struct {
	Mode            string `json:"mode"`
	ServerDataURL   string `json:"server_data_url"`
	GameDataURL     string `json:"game_data_url"`
	BundleVersion   string `json:"bundle_version"`
	GameDataVersion string `json:"game_data_version"`
}

func Default() Config {
	return Config{
		Mode:          ModeOfficial,
		ServerDataURL: OfficialServerDataURL,
		GameDataURL:   OfficialGameDataURL,
	}
}

// Load reads one explicit resources.json. Unknown fields and trailing JSON are
// rejected so a misspelled URL key cannot silently fall back to another CDN.
func Load(path string) (Config, error) {
	if err := commonconfig.Ensure(path, Default(), commonconfig.WriteOptions{}); err != nil {
		return Config{}, fmt.Errorf("resource policy: initialize %s: %w", path, err)
	}
	cfg, err := commonconfig.Load(path, Config{}, commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true})
	if err != nil {
		return Config{}, fmt.Errorf("resource policy: load %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("resource policy: %s: %w", path, err)
	}
	return cfg, nil
}

// BesideExecutable returns the production configuration location.
func BesideExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resource policy: resolve server executable: %w", err)
	}
	return filepath.Join(filepath.Dir(executable), FileName), nil
}

func (c Config) Validate() error {
	switch c.Mode {
	case ModeOfficial:
		if c.ServerDataURL == "" {
			c.ServerDataURL = OfficialServerDataURL
		}
		if c.GameDataURL == "" {
			c.GameDataURL = OfficialGameDataURL
		}
		if c.ServerDataURL != OfficialServerDataURL || c.GameDataURL != OfficialGameDataURL {
			return errors.New("official mode must use the versioned official resource origins")
		}
	case ModeServer:
		if c.ServerDataURL == "" || c.GameDataURL == "" {
			return errors.New("server_data_url and game_data_url are required")
		}
	default:
		return fmt.Errorf("unsupported mode %q", c.Mode)
	}
	if err := validateBaseURL("server_data_url", c.serverDataURL()); err != nil {
		return err
	}
	return validateBaseURL("game_data_url", c.gameDataURL())
}

func (c Config) Public(bundleVersion, gameDataVersion string) Public {
	return Public{
		Mode:            c.Mode,
		ServerDataURL:   c.serverDataURL(),
		GameDataURL:     c.gameDataURL(),
		BundleVersion:   bundleVersion,
		GameDataVersion: gameDataVersion,
	}
}

func (c Config) serverDataURL() string {
	if c.Mode == ModeOfficial && c.ServerDataURL == "" {
		return OfficialServerDataURL
	}
	return c.ServerDataURL
}

func (c Config) gameDataURL() string {
	if c.Mode == ModeOfficial && c.GameDataURL == "" {
		return OfficialGameDataURL
	}
	return c.GameDataURL
}

func validateBaseURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL without credentials, query, or fragment", name)
	}
	if strings.HasSuffix(u.Path, "/") {
		return fmt.Errorf("%s must not have a trailing slash", name)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("%s must use HTTPS outside loopback development", name)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
