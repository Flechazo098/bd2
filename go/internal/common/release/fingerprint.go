package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"bd2server/internal/common/versionconfig"
)

const FingerprintFileName = "UPDATE_FINGERPRINT.json"
const fingerprintDomain = "bd2-update-fingerprint-v1\n"

func (g GameResources) ResourceID() string {
	raw, _ := canonicalJSON(g.ResourceFiles)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (g GameResources) ContentID() string {
	raw, _ := canonicalJSON(g)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type Fingerprint struct {
	ID                  string            `json:"id"`
	ServerVersion       string            `json:"server_version"`
	ClientVersion       string            `json:"client_version"`
	RequireClientUpdate bool              `json:"require_client_update"`
	RequireGameUpdate   bool              `json:"require_game_update"`
	GameFingerprint     string            `json:"game_fingerprint,omitempty"`
	MinimumPlugins      map[string]string `json:"minimum_plugins"`
	Game                *GameResources    `json:"game,omitempty"`
}

type GameResources struct {
	GameVersion     string         `json:"game_version"`
	GameDataVersion string         `json:"game_data_version"`
	BundleVersion   string         `json:"bundle_version"`
	Platform        string         `json:"platform"`
	ServerOrigin    string         `json:"server_origin"`
	BuildOrigins    []string       `json:"build_origins"`
	ResourceOrigins []string       `json:"resource_origins"`
	GamePath        string         `json:"game_path"`
	GameSHA256      string         `json:"game_sha256"`
	GameBytes       int64          `json:"game_bytes"`
	DataSHA256      string         `json:"data_sha256"`
	DataBytes       int64          `json:"data_bytes"`
	CatalogSHA256   string         `json:"catalog_sha256"`
	CatalogBytes    int64          `json:"catalog_bytes"`
	CatalogHash     string         `json:"catalog_hash"`
	ResourceFiles   []ResourceFile `json:"resource_files,omitempty"`
}

type ResourceFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (g GameResources) Validate() error {
	gameVersion, err := ParseVersion(g.GameVersion)
	if err != nil || gameVersion.HasGame {
		return errors.New("release: invalid game version")
	}
	if len(g.BundleVersion) != 14 || len(g.GameDataVersion) != 14 || strings.Trim(g.BundleVersion+g.GameDataVersion, "0123456789") != "" {
		return errors.New("release: invalid resource versions")
	}
	if g.Platform != "StandaloneWindows64" || !strings.HasPrefix(g.GamePath, "/Build/") || path.Clean(g.GamePath) != g.GamePath || strings.ContainsAny(g.GamePath, "\\?#") {
		return errors.New("release: unsupported game download path or platform")
	}
	for _, f := range []struct {
		sum  string
		size int64
	}{{g.GameSHA256, g.GameBytes}, {g.DataSHA256, g.DataBytes}, {g.CatalogSHA256, g.CatalogBytes}} {
		if !digestPattern.MatchString(f.sum) || f.size <= 0 {
			return errors.New("release: game resources require pinned SHA-256 and positive sizes")
		}
	}
	if len(g.CatalogHash) != 32 || strings.Trim(strings.ToLower(g.CatalogHash), "0123456789abcdef") != "" {
		return errors.New("release: invalid catalog hash")
	}
	if len(g.BuildOrigins) == 0 || len(g.ResourceOrigins) == 0 {
		return errors.New("release: game resources have no download origins")
	}
	for _, origin := range append(append([]string{}, g.BuildOrigins...), g.ResourceOrigins...) {
		u, err := url.Parse(origin)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("release: invalid game resource origin")
		}
		if _, err := Resolve(origin, origin); err != nil {
			return err
		}
	}
	if len(g.ResourceFiles) == 0 || len(g.ResourceFiles) > 100000 {
		return errors.New("release: game resources require a bounded file inventory")
	}
	seen := map[string]bool{}
	pins := map[string]ResourceFile{}
	for _, f := range g.ResourceFiles {
		allowed := strings.HasPrefix(f.Path, "ServerData/"+g.Platform+"/HD/"+g.BundleVersion+"/") || strings.HasPrefix(f.Path, "GameData/"+g.GameDataVersion+"/")
		key := strings.ToLower(f.Path)
		if !allowed || path.Clean(f.Path) != f.Path || strings.ContainsAny(f.Path, "\\:\x00") || f.Size < 0 || !digestPattern.MatchString(f.SHA256) || seen[key] {
			return fmt.Errorf("release: invalid resource inventory entry %q", f.Path)
		}
		seen[key] = true
		for part := range strings.SplitSeq(f.Path, "/") {
			if strings.TrimRight(part, ". ") != part {
				return fmt.Errorf("release: unsafe resource path %q", f.Path)
			}
		}
		pins[f.Path] = f
	}
	base := "ServerData/" + g.Platform + "/HD/" + g.BundleVersion + "/"
	data := "GameData/" + g.GameDataVersion + "/"
	for _, name := range []string{base + "catalog_alpha.hash", data + "design.version", data + "release/common-dbdata.info"} {
		if _, ok := pins[name]; !ok {
			return fmt.Errorf("release: missing required resource %s", name)
		}
	}
	for _, expected := range []ResourceFile{{Path: base + "catalog_alpha.json", Size: g.CatalogBytes, SHA256: g.CatalogSHA256}, {Path: data + "release/common-dbdata.bin", Size: g.DataBytes, SHA256: g.DataSHA256}} {
		if pins[expected.Path] != expected {
			return fmt.Errorf("release: conflicting signed resource %s", expected.Path)
		}
	}
	return nil
}

func (f Fingerprint) Validate() error {
	server, err := ParseVersion(f.ServerVersion)
	if err != nil || server.Kind != "server" {
		return errors.New("release: invalid fingerprint server version")
	}
	client, err := ParseVersion(f.ClientVersion)
	if err != nil || client.Kind != "client" {
		return errors.New("release: invalid fingerprint client version")
	}
	if server.Game != client.Game {
		return errors.New("release: fingerprint client and server game versions differ")
	}
	if f.Game != nil && f.Game.GameVersion != client.GameVersion() {
		return errors.New("release: fingerprint game version differs from components")
	}
	for id, version := range f.MinimumPlugins {
		v, e := ParseVersion(version)
		if e != nil || !knownPlayerPlugin(id) || v.HasGame {
			return errors.New("release: invalid fingerprint plugin version")
		}
	}
	if f.RequireClientUpdate && len(f.MinimumPlugins) == 0 {
		return errors.New("release: required client update has no plugin requirements")
	}
	if f.RequireGameUpdate && f.Game == nil {
		return errors.New("release: required game update has no resources")
	}
	if f.Game != nil {
		if err := f.Game.Validate(); err != nil {
			return err
		}
		if f.GameFingerprint != f.Game.ContentID() {
			return errors.New("release: game fingerprint differs from signed resource content")
		}
	}
	digest, err := f.digest()
	if err != nil {
		return err
	}
	if f.ID != digest {
		return errors.New("release: fingerprint identity does not match its content")
	}
	return nil
}

func knownPlayerPlugin(id string) bool {
	switch id {
	case "local_identity", "login_ui", "cash_shop", "fchat":
		return true
	default:
		return false
	}
}

func (f Fingerprint) digest() (string, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	delete(fields, "id")
	raw, err = canonicalJSON(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err = d.Decode(&fields); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func (f Fingerprint) MatchesVersions(v versionconfig.Config) error {
	if f.ServerVersion != v.ServerVersion || f.ClientVersion != v.ClientVersion {
		return errors.New("release: fingerprint does not match installed component versions")
	}
	if f.Game != nil && (f.Game.GameVersion != v.GameVersion || f.Game.BundleVersion != v.BundleVersion || f.Game.GameDataVersion != v.GameDataVersion) {
		return errors.New("release: fingerprint does not match installed game resource versions")
	}
	for id, version := range f.MinimumPlugins {
		if v.Plugins.ByID()[id] != version {
			return fmt.Errorf("release: fingerprint plugin %s does not match installed version", id)
		}
	}
	return nil
}

func VerifyFingerprint(raw []byte, keys []TrustedKey) (Fingerprint, error) {
	var envelope struct {
		KeyID       string          `json:"key_id"`
		Signature   string          `json:"signature"`
		Fingerprint json.RawMessage `json:"fingerprint"`
	}
	if err := decodeStrict(raw, &envelope); err != nil {
		return Fingerprint{}, err
	}
	public, err := trusted(envelope.KeyID, keys)
	if err != nil {
		return Fingerprint{}, err
	}
	sig, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(public, append([]byte(fingerprintDomain), envelope.Fingerprint...), sig) {
		return Fingerprint{}, errors.New("release: update fingerprint signature is invalid")
	}
	var fingerprint Fingerprint
	if err := decodeStrict(envelope.Fingerprint, &fingerprint); err != nil {
		return Fingerprint{}, err
	}
	if err := fingerprint.Validate(); err != nil {
		return Fingerprint{}, err
	}
	return fingerprint, nil
}

func LoadFingerprint(file string, keys []TrustedKey) (*Fingerprint, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	f, err := VerifyFingerprint(raw, keys)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func decodeStrict(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("release: trailing JSON content")
	}
	return nil
}
