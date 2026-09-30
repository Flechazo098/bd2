// Package authconfig loads the server-authoritative authentication policy.
package authconfig

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const FileName = "authentication.json"

// TODO: Replace the executable-adjacent file with a small authenticated
// configuration API while keeping this policy server-authoritative.
type Config struct {
	Mode         string                    `json:"mode"`
	PublicURL    string                    `json:"public_url,omitempty"`
	MasterKeyEnv string                    `json:"master_key_env,omitempty"`
	Providers    map[string]ProviderConfig `json:"providers,omitempty"`
	Session      SessionConfig             `json:"session,omitempty"`
}

type ProviderConfig struct {
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"`
}

type SessionConfig struct {
	AccessTTL            string `json:"access_ttl,omitempty"`
	RefreshTTL           string `json:"refresh_ttl,omitempty"`
	DeviceTransactionTTL string `json:"device_transaction_ttl,omitempty"`
}

type Runtime struct {
	Config
	PublicURLParsed *url.URL
	MasterKey       []byte
	ProviderSecrets map[string]string
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	DeviceTTL       time.Duration
}

type Public struct {
	Mode      string   `json:"mode"`
	Providers []string `json:"providers"`
}

func Load(path string) (Config, error) {
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return Config{}, fmt.Errorf("authconfig: resolve path: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("authconfig: open %s: %w", path, err)
	}
	defer file.Close()
	var config Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("authconfig: decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Config{}, fmt.Errorf("authconfig: trailing JSON in %s", path)
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("authconfig: trailing data in %s: %w", path, err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("authconfig: %s: %w", path, err)
	}
	return config, nil
}

func BesideExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("authconfig: resolve executable: %w", err)
	}
	return filepath.Join(filepath.Dir(executable), FileName), nil
}

func (c Config) Validate() error {
	if c.Mode != "local" && c.Mode != "oauth" {
		return errors.New("mode must be local or oauth")
	}
	if c.Mode == "local" {
		if c.PublicURL != "" || c.MasterKeyEnv != "" || len(c.Providers) != 0 {
			return errors.New("local mode must not configure OAuth")
		}
		return validateTTLs(c.Session)
	}
	if c.MasterKeyEnv == "" || c.PublicURL == "" || len(c.Providers) == 0 {
		return errors.New("oauth mode requires public_url, master_key_env, and providers")
	}
	publicURL, err := url.Parse(c.PublicURL)
	if err != nil || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" || publicURL.Path != "" {
		return errors.New("public_url must be an absolute origin without path, query, fragment, or user info")
	}
	localhost := publicURL.Hostname() == "127.0.0.1" || publicURL.Hostname() == "localhost" || publicURL.Hostname() == "::1"
	if publicURL.Scheme != "https" && !(localhost && publicURL.Scheme == "http") {
		return errors.New("public_url must use HTTPS except on localhost")
	}
	for name, provider := range c.Providers {
		if name != "discord" && name != "google" {
			return fmt.Errorf("unsupported provider %q", name)
		}
		if provider.ClientID == "" || provider.ClientSecretEnv == "" {
			return fmt.Errorf("provider %q requires client_id and client_secret_env", name)
		}
	}
	return validateTTLs(c.Session)
}

func validateTTLs(session SessionConfig) error {
	for name, raw := range map[string]string{"access_ttl": session.AccessTTL, "refresh_ttl": session.RefreshTTL, "device_transaction_ttl": session.DeviceTransactionTTL} {
		if raw != "" {
			if value, err := time.ParseDuration(raw); err != nil || value <= 0 {
				return fmt.Errorf("%s must be a positive Go duration", name)
			}
		}
	}
	return nil
}

func (c Config) ResolveEnvironment() (Runtime, error) {
	if err := c.Validate(); err != nil {
		return Runtime{}, err
	}
	runtime := Runtime{Config: c, ProviderSecrets: make(map[string]string), AccessTTL: 15 * time.Minute, RefreshTTL: 30 * 24 * time.Hour, DeviceTTL: 10 * time.Minute}
	if c.Session.AccessTTL != "" {
		runtime.AccessTTL, _ = time.ParseDuration(c.Session.AccessTTL)
	}
	if c.Session.RefreshTTL != "" {
		runtime.RefreshTTL, _ = time.ParseDuration(c.Session.RefreshTTL)
	}
	if c.Session.DeviceTransactionTTL != "" {
		runtime.DeviceTTL, _ = time.ParseDuration(c.Session.DeviceTransactionTTL)
	}
	if c.Mode == "local" {
		return runtime, nil
	}
	runtime.PublicURLParsed, _ = url.Parse(c.PublicURL)
	key, err := base64.StdEncoding.DecodeString(os.Getenv(c.MasterKeyEnv))
	if err != nil || len(key) != 32 {
		return Runtime{}, fmt.Errorf("authconfig: %s must contain a base64-encoded 32-byte key", c.MasterKeyEnv)
	}
	runtime.MasterKey = key
	for name, provider := range c.Providers {
		secret := os.Getenv(provider.ClientSecretEnv)
		if strings.TrimSpace(secret) == "" {
			return Runtime{}, fmt.Errorf("authconfig: provider %s secret environment %s is empty", name, provider.ClientSecretEnv)
		}
		runtime.ProviderSecrets[name] = secret
	}
	return runtime, nil
}

func (c Config) Public() Public {
	view := Public{Mode: c.Mode, Providers: []string{}}
	for _, name := range []string{"discord", "google"} {
		if _, ok := c.Providers[name]; ok {
			view.Providers = append(view.Providers, name)
		}
	}
	return view
}
