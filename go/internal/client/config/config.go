// Package config owns the client-side connection settings consumed by the
// standalone setup tool and the Local Identity plugin.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	clientlayout "bd2server/internal/client/layout"
	commonconfig "bd2server/internal/common/config"
)

const (
	SchemaVersion = 2
	FileName      = "bd2.client.json"

	CDNOfficial CDNMode = "official"
	CDNLocal    CDNMode = "local"
	CDNServer   CDNMode = "server"
)

type CDNMode string

type Settings struct {
	ProxyURL               string   `json:"proxy_url,omitempty"`
	SchemaVersion          int      `json:"schema_version"`
	ServerOrigin           string   `json:"server_origin"`
	CDNMode                CDNMode  `json:"cdn_mode"`
	LocalResourceDirectory string   `json:"local_resource_directory,omitempty"`
	Runtime                *Runtime `json:"runtime,omitempty"`
}

type Runtime struct {
	ClientVersion       string `json:"client_version"`
	UpdateFingerprint   string `json:"update_fingerprint"`
	ResourceFingerprint string `json:"resource_fingerprint"`
	GameVersion         string `json:"game_version"`
	BundleVersion       string `json:"bundle_version"`
	GameDataVersion     string `json:"game_data_version"`
}

func Path(gameDir string) string {
	if installation, err := clientlayout.Resolve(gameDir); err == nil {
		return filepath.Join(installation.Config, FileName)
	}
	return filepath.Join(filepath.Clean(gameDir), "BepInEx", "config", FileName)
}

func Normalize(in Settings) (Settings, error) {
	origin, err := NormalizeOrigin(in.ServerOrigin)
	if err != nil {
		return Settings{}, err
	}
	proxyURL, err := NormalizeProxyURL(in.ProxyURL)
	if err != nil {
		return Settings{}, err
	}
	localDirectory := strings.TrimSpace(in.LocalResourceDirectory)
	switch in.CDNMode {
	case CDNOfficial, CDNServer:
		if localDirectory != "" {
			return Settings{}, errors.New("client config: local_resource_directory is only valid in local mode")
		}
	case CDNLocal:
		if localDirectory == "" {
			return Settings{}, errors.New("client config: local mode requires local_resource_directory")
		}
		localDirectory, err = filepath.Abs(filepath.Clean(localDirectory))
		if err != nil {
			return Settings{}, fmt.Errorf("client config: resolve local resource directory: %w", err)
		}
	default:
		return Settings{}, fmt.Errorf("client config: unsupported CDN mode %q", in.CDNMode)
	}
	return Settings{
		SchemaVersion:          SchemaVersion,
		ProxyURL:               proxyURL,
		ServerOrigin:           origin,
		CDNMode:                in.CDNMode,
		LocalResourceDirectory: localDirectory,
		Runtime:                in.Runtime,
	}, nil
}

func NormalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("client config: parse server origin: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("client config: server address must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("client config: server address must include a host")
	}
	if parsed.User != nil {
		return "", errors.New("client config: credentials are not allowed in the server address")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("client config: server address cannot contain a query or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("client config: enter only the server origin, without /game or another path")
	}
	if parsed.Scheme == "http" && !isLoopback(parsed.Hostname()) {
		return "", errors.New("client config: non-loopback servers must use https")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

// NormalizeProxyURL accepts only an explicit HTTP proxy endpoint. Errors never echo input.
func NormalizeProxyURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	invalid := errors.New("client config: proxy must be an HTTP URL with a host and numeric port (1-65535), without credentials, path, query or fragment")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#") || (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" {
		return "", invalid
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host == "" || port == "" || strings.ContainsAny(host, " \t\r\n%") {
		return "", invalid
	}
	for _, ch := range port {
		if ch < '0' || ch > '9' {
			return "", invalid
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", invalid
	}
	parsed.Host = net.JoinHostPort(host, strconv.Itoa(number))
	parsed.Path = ""
	return parsed.String(), nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func Save(gameDir string, in Settings) (Settings, error) {
	settings, err := Normalize(in)
	if err != nil {
		return Settings{}, err
	}
	if current, err := Load(gameDir); err == nil && reflect.DeepEqual(current, settings) {
		return settings, nil
	}
	path := Path(gameDir)
	if err := commonconfig.Save(path, settings, commonconfig.WriteOptions{}); err != nil {
		return Settings{}, fmt.Errorf("client config: save: %w", err)
	}
	return settings, nil
}

func Load(gameDir string) (Settings, error) {
	settings, err := commonconfig.Load(Path(gameDir), Settings{}, commonconfig.DecodeOptions{DisallowUnknownFields: true})
	if err != nil {
		return Settings{}, fmt.Errorf("client config: load: %w", err)
	}
	if settings.SchemaVersion != SchemaVersion {
		return Settings{}, fmt.Errorf("client config: unsupported schema_version %d", settings.SchemaVersion)
	}
	return Normalize(settings)
}
