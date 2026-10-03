package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const ReleaseFileName = "versions.json"

var (
	gameVersionPattern     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	releaseVersionPattern  = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+)\+(client|server)\.([0-9]+\.[0-9]+\.[0-9]+)$`)
	resourceVersionPattern = regexp.MustCompile(`^[0-9]{14}$`)
)

// ReleaseVersions separates the official game compatibility version from the
// independently evolving client and server software release versions. The
// release package carries the authoritative versions.json beside bd2client.
type ReleaseVersions struct {
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
	} `json:"plugins"`
}

func LoadReleaseVersions(path string) (ReleaseVersions, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ReleaseVersions{}, fmt.Errorf("read client release versions: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var versions ReleaseVersions
	if err := decoder.Decode(&versions); err != nil {
		return ReleaseVersions{}, fmt.Errorf("decode client release versions: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ReleaseVersions{}, errors.New("client release versions must contain exactly one JSON object")
	}
	if !gameVersionPattern.MatchString(versions.GameVersion) {
		return ReleaseVersions{}, fmt.Errorf("invalid game_version %q", versions.GameVersion)
	}
	if !validReleaseVersion(versions.ClientVersion, versions.GameVersion, "client") {
		return ReleaseVersions{}, fmt.Errorf("invalid client_version %q", versions.ClientVersion)
	}
	if !validReleaseVersion(versions.ServerVersion, versions.GameVersion, "server") {
		return ReleaseVersions{}, fmt.Errorf("invalid server_version %q", versions.ServerVersion)
	}
	if !resourceVersionPattern.MatchString(versions.BundleVersion) || !resourceVersionPattern.MatchString(versions.GameDataVersion) {
		return ReleaseVersions{}, errors.New("bundle_version and game_data_version must be 14-digit timestamps")
	}
	if versions.SeedDirectory == "" ||
		versions.Plugins.LocalIdentity == "" || versions.Plugins.LoginUI == "" || versions.Plugins.CaptureEnvironment == "" {
		return ReleaseVersions{}, errors.New("client release versions are incomplete")
	}
	return versions, nil
}

func validReleaseVersion(value, game, component string) bool {
	match := releaseVersionPattern.FindStringSubmatch(value)
	return len(match) == 4 && match[1] == game && match[2] == component
}

func ReleaseVersionsBesideExecutable() (ReleaseVersions, error) {
	executable, err := os.Executable()
	if err != nil {
		return ReleaseVersions{}, fmt.Errorf("locate bd2client executable: %w", err)
	}
	return LoadReleaseVersions(filepath.Join(filepath.Dir(executable), ReleaseFileName))
}
