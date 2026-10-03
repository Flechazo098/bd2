// Package setup implements the filesystem and network operations exposed by
// bd2client. It contains no server runtime dependencies.
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	clientconfig "bd2server/internal/client/config"
	"bd2server/internal/client/introdb"
	clientlayout "bd2server/internal/client/layout"
	"bd2server/internal/client/plugin"
)

const PatchPlaceholder = "http://127.0.0.1:8080/game/"

type GameStatus struct {
	GameDirectory string `json:"game_directory"`
	ClientVersion string `json:"client_version"`
	Executable    bool   `json:"executable"`
	Resources     bool   `json:"resources"`
	BepInEx       bool   `json:"bepinex"`
	Config        bool   `json:"config"`
	PatchedURL    string `json:"patched_url,omitempty"`
}

var clientVersionPattern = regexp.MustCompile(`(?:^|\x00)([0-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,3})(?:\x00)`)

type ResourcePolicy struct {
	Mode            clientconfig.CDNMode `json:"mode"`
	ServerDataURL   string               `json:"server_data_url"`
	GameDataURL     string               `json:"game_data_url"`
	BundleVersion   string               `json:"bundle_version"`
	GameDataVersion string               `json:"game_data_version"`
	LocalDirectory  string               `json:"local_directory,omitempty"`
}

type InstallResult struct {
	LocalIdentity plugin.Result `json:"local_identity"`
	LoginUI       plugin.Result `json:"login_ui"`
}

func Inspect(gameDir string, versions clientconfig.ReleaseVersions) (GameStatus, error) {
	if strings.TrimSpace(gameDir) == "" {
		return GameStatus{}, errors.New("select the Brown Dust II installation directory")
	}
	installation, err := clientlayout.Resolve(gameDir)
	if err != nil {
		return GameStatus{}, err
	}
	status := GameStatus{GameDirectory: installation.Root}
	status.Executable = regularFile(installation.Executable)
	status.Resources = regularFile(installation.Resources)
	status.BepInEx = regularFile(filepath.Join(installation.BepInEx, "core", "BepInEx.dll"))
	status.Config = regularFile(filepath.Join(installation.Config, clientconfig.FileName))
	status.ClientVersion, err = detectClientVersion(installation.Managers)
	if err != nil {
		return status, err
	}
	if status.ClientVersion != versions.GameVersion {
		return status, fmt.Errorf("unsupported Brown Dust II client version %s; this bd2client release requires %s", status.ClientVersion, versions.GameVersion)
	}
	if verified, verifyErr := introdb.VerifyClient(installation.Root); verifyErr == nil {
		status.PatchedURL = verified.URL
	}
	return status, nil
}

func SaveSettings(gameDir string, settings clientconfig.Settings, versions clientconfig.ReleaseVersions) (clientconfig.Settings, error) {
	if _, err := Inspect(gameDir, versions); err != nil {
		return clientconfig.Settings{}, err
	}
	normalized, err := clientconfig.Normalize(settings)
	if err != nil {
		return clientconfig.Settings{}, err
	}
	if normalized.CDNMode == clientconfig.CDNLocal {
		if err := installCurrentLocalCatalog(gameDir, normalized.LocalResourceDirectory, versions.BundleVersion); err != nil {
			return clientconfig.Settings{}, err
		}
	}
	return clientconfig.Save(gameDir, normalized)
}

func Patch(gameDir string, settings clientconfig.Settings, versions clientconfig.ReleaseVersions) (introdb.Result, error) {
	if _, err := SaveSettings(gameDir, settings, versions); err != nil {
		return introdb.Result{}, err
	}
	result, err := introdb.PatchClient(gameDir, PatchPlaceholder)
	if err != nil {
		return introdb.Result{}, err
	}
	if _, err := introdb.VerifyClient(gameDir); err != nil {
		return introdb.Result{}, fmt.Errorf("verify patched client resources: %w", err)
	}
	if _, err := disableLegacyPlugin(gameDir); err != nil {
		return introdb.Result{}, err
	}
	return result, nil
}

func InstallPlugins(
	gameDir string,
	settings clientconfig.Settings,
	versions clientconfig.ReleaseVersions,
	localIdentitySource string,
	loginUISource string,
) (InstallResult, error) {
	status, err := Inspect(gameDir, versions)
	if err != nil {
		return InstallResult{}, err
	}
	if !status.BepInEx {
		return InstallResult{}, fmt.Errorf("BepInEx is not installed; install it from %s before installing the plugins", plugin.BepInExReleasesURL)
	}
	if _, err := SaveSettings(gameDir, settings, versions); err != nil {
		return InstallResult{}, err
	}
	localSource, err := plugin.ResolvePackaged(plugin.LocalIdentity, localIdentitySource)
	if err != nil {
		return InstallResult{}, err
	}
	loginSource, err := plugin.ResolvePackaged(plugin.LoginUI, loginUISource)
	if err != nil {
		return InstallResult{}, err
	}
	local, err := plugin.Install(plugin.LocalIdentity, gameDir, localSource)
	if err != nil {
		return InstallResult{}, err
	}
	login, err := plugin.Install(plugin.LoginUI, gameDir, loginSource)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{LocalIdentity: local, LoginUI: login}, nil
}

func FetchResourcePolicy(ctx context.Context, client *http.Client, settings clientconfig.Settings, versions clientconfig.ReleaseVersions) (ResourcePolicy, error) {
	normalized, err := clientconfig.Normalize(settings)
	if err != nil {
		return ResourcePolicy{}, err
	}
	if normalized.CDNMode == clientconfig.CDNOfficial {
		return ResourcePolicy{Mode: clientconfig.CDNOfficial}, nil
	}
	if normalized.CDNMode == clientconfig.CDNLocal {
		root, err := inspectLocalResourceDirectory(normalized.LocalResourceDirectory, versions)
		if err != nil {
			return ResourcePolicy{}, err
		}
		return ResourcePolicy{
			Mode:            clientconfig.CDNLocal,
			ServerDataURL:   localResourceURL(filepath.Join(root, "ServerData")),
			GameDataURL:     localResourceURL(filepath.Join(root, "GameData")),
			BundleVersion:   versions.BundleVersion,
			GameDataVersion: versions.GameDataVersion,
			LocalDirectory:  root,
		}, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	body, err := json.Marshal(map[string]clientconfig.CDNMode{"cdn_mode": normalized.CDNMode})
	if err != nil {
		return ResourcePolicy{}, err
	}
	endpoint := normalized.ServerOrigin + "/client/resources"
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return ResourcePolicy{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return ResourcePolicy{}, fmt.Errorf("request server resource policy: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 64<<10)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return ResourcePolicy{}, fmt.Errorf("read server resource policy response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 300 {
			message = message[:300]
		}
		return ResourcePolicy{}, fmt.Errorf("server rejected CDN mode %s (HTTP %d): %s", normalized.CDNMode, response.StatusCode, message)
	}
	var policy ResourcePolicy
	if err := json.Unmarshal(responseBody, &policy); err != nil {
		return ResourcePolicy{}, fmt.Errorf("decode server resource policy: %w", err)
	}
	if policy.Mode != normalized.CDNMode {
		return ResourcePolicy{}, fmt.Errorf("server returned CDN mode %q, expected %q", policy.Mode, normalized.CDNMode)
	}
	if err := validatePublicURL("ServerData", policy.ServerDataURL); err != nil {
		return ResourcePolicy{}, err
	}
	if err := validatePublicURL("GameData", policy.GameDataURL); err != nil {
		return ResourcePolicy{}, err
	}
	if policy.BundleVersion == "" || policy.GameDataVersion == "" {
		return ResourcePolicy{}, errors.New("server resource policy is missing bundle_version or game_data_version")
	}
	if policy.BundleVersion != versions.BundleVersion || policy.GameDataVersion != versions.GameDataVersion {
		return ResourcePolicy{}, fmt.Errorf(
			"server resource versions do not match this client release: bundle=%s (want %s), GameData=%s (want %s)",
			policy.BundleVersion, versions.BundleVersion, policy.GameDataVersion, versions.GameDataVersion,
		)
	}
	return policy, nil
}

func inspectLocalResourceDirectory(raw string, versions clientconfig.ReleaseVersions) (string, error) {
	root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(raw)))
	if err != nil {
		return "", fmt.Errorf("resolve local resource directory: %w", err)
	}
	for _, relative := range []string{
		filepath.Join("ServerData", "StandaloneWindows64", "HD", versions.BundleVersion, "catalog_alpha.json"),
		filepath.Join("ServerData", "StandaloneWindows64", "HD", versions.BundleVersion, "catalog_alpha.hash"),
		filepath.Join("GameData", versions.GameDataVersion, "release", "common-dbdata.info"),
		filepath.Join("GameData", versions.GameDataVersion, "release", "common-dbdata.bin"),
	} {
		info, statErr := os.Stat(filepath.Join(root, relative))
		if statErr != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("local resource directory is missing %s", relative)
		}
	}
	return root, nil
}

func detectClientVersion(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read Brown Dust II client version metadata: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("Brown Dust II client version metadata is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > 64<<20 {
		return "", fmt.Errorf("Brown Dust II client version metadata has an invalid size: %d", info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Brown Dust II client version metadata: %w", err)
	}
	matches := clientVersionPattern.FindAllSubmatch(data, -1)
	versions := make(map[string]struct{})
	for _, match := range matches {
		versions[string(match[1])] = struct{}{}
	}
	if len(versions) != 1 {
		return "", fmt.Errorf("could not identify one unambiguous Brown Dust II client version in %s", path)
	}
	for version := range versions {
		return version, nil
	}
	panic("unreachable")
}

func localResourceURL(path string) string {
	slashed := filepath.ToSlash(filepath.Clean(path))
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

func validatePublicURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("server returned an invalid %s URL", name)
	}
	if parsed.Scheme == "http" && !resourceLoopback(parsed.Hostname()) {
		return fmt.Errorf("server returned an invalid %s URL", name)
	}
	return nil
}

func resourceLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func disableLegacyPlugin(gameDir string) (string, error) {
	installation, err := clientlayout.Resolve(gameDir)
	if err != nil {
		return "", err
	}
	source := filepath.Join(installation.Plugins, "PluginLocalRes.dll")
	destination := filepath.Join(installation.Disabled, "PluginLocalRes.dll")
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("inspect legacy local resource plugin: %w", err)
	}
	if _, err := os.Stat(destination); err == nil {
		return "", errors.New("the legacy local resource plugin exists in both active and disabled directories; remove one copy manually")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(source, destination); err != nil {
		return "", fmt.Errorf("disable legacy local resource plugin: %w", err)
	}
	return destination, nil
}
