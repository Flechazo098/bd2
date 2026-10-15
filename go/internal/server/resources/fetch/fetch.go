// Package resourcefetch mirrors versioned official resources for operators
// that choose a self-hosted CDN. Normal server startup only fetches GameData
// needed by server logic; this package is used by the explicit resources command.
package resourcefetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"bd2server/internal/common/release"
	"bd2server/internal/common/resourcecatalog"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/resources/policy"
)

var catalogHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
var resourceVersionPattern = regexp.MustCompile(`^[0-9]{14}$`)

type Options struct {
	OutputRoot      string
	Platform        string
	BundleVersion   string
	GameDataVersion string
	Client          *http.Client
	Progress        func(string)
	Pinned          *release.GameResources
}

type Manifest struct {
	InventoryID     string         `json:"inventory_id,omitempty"`
	VerifiedFiles   []VerifiedFile `json:"verified_files,omitempty"`
	BundleVersion   string         `json:"bundle_version"`
	GameDataVersion string         `json:"game_data_version"`
	Platform        string         `json:"platform"`
	ServerData      struct {
		CatalogHash string `json:"catalog_hash"`
		Bundles     int    `json:"bundles"`
		Bytes       int64  `json:"bytes"`
	} `json:"server_data"`
	GameData gamedata.Result `json:"game_data"`
}

type VerifiedFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	SHA256   string `json:"sha256"`
}

func Fetch(ctx context.Context, options Options) (Manifest, error) {
	if options.Pinned != nil {
		return fetchPinned(ctx, options)
	}
	if options.OutputRoot == "" || options.BundleVersion == "" || options.GameDataVersion == "" {
		return Manifest{}, errors.New("resource fetch: output and resource versions are required")
	}
	if !resourceVersionPattern.MatchString(options.BundleVersion) || !resourceVersionPattern.MatchString(options.GameDataVersion) {
		return Manifest{}, errors.New("resource fetch: resource versions must be 14-digit timestamps")
	}
	if options.Platform == "" {
		options.Platform = "StandaloneWindows64"
	}
	if strings.ContainsAny(options.Platform, `/\\`) {
		return Manifest{}, errors.New("resource fetch: platform must be one path segment")
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 30 * time.Minute}
	}
	root, err := filepath.Abs(filepath.Clean(options.OutputRoot))
	if err != nil {
		return Manifest{}, fmt.Errorf("resource fetch: resolve output: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("resource fetch: create output: %w", err)
	}

	gameData, _, err := gamedata.Ensure(ctx, options.Client, filepath.Join(root, "GameData"), options.GameDataVersion, resourcepolicy.OfficialGameDataURL)
	if err != nil {
		return Manifest{}, err
	}
	designVersionPath := filepath.Join(root, "GameData", options.GameDataVersion, "design.version")
	if info, statErr := os.Stat(designVersionPath); statErr != nil || info.IsDir() || info.Size() == 0 {
		designVersion, downloadErr := downloadBytes(ctx, options.Client, resourcepolicy.OfficialGameDataURL+"/"+options.GameDataVersion+"/design.version", 1<<20)
		if downloadErr != nil {
			return Manifest{}, downloadErr
		}
		if len(designVersion) == 0 {
			return Manifest{}, errors.New("resource fetch: empty GameData design.version")
		}
		if err := atomicWrite(designVersionPath, designVersion, 0o644); err != nil {
			return Manifest{}, err
		}
	}
	options.log(fmt.Sprintf("GameData %s verified (%d entries)", options.GameDataVersion, gameData.EntryCount))

	hash, bundles, total, err := fetchServerData(ctx, options, root)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{BundleVersion: options.BundleVersion, GameDataVersion: options.GameDataVersion, Platform: options.Platform, GameData: gameData}
	manifest.ServerData.CatalogHash = hash
	manifest.ServerData.Bundles = bundles
	manifest.ServerData.Bytes = total
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	encoded = append(encoded, '\n')
	if err := atomicWrite(filepath.Join(root, "resource-fetch-manifest.json"), encoded, 0o644); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func fetchServerData(ctx context.Context, options Options, root string) (string, int, int64, error) {
	base := resourcepolicy.OfficialServerDataURL + "/" + options.Platform + "/HD/" + options.BundleVersion
	catalog, err := downloadBytes(ctx, options.Client, base+"/catalog_alpha.json", 64<<20)
	if err != nil {
		return "", 0, 0, err
	}
	hashBytes, err := downloadBytes(ctx, options.Client, base+"/catalog_alpha.hash", 1024)
	if err != nil {
		return "", 0, 0, err
	}
	hash := strings.TrimSpace(string(hashBytes))
	if !catalogHashPattern.MatchString(hash) {
		return "", 0, 0, fmt.Errorf("resource fetch: invalid catalog hash %q", hash)
	}
	paths, err := resourcecatalog.BundlePaths(catalog)
	if err != nil {
		return "", 0, 0, err
	}
	destination := filepath.Join(root, "ServerData", options.Platform, "HD", options.BundleVersion)
	if err := atomicWrite(filepath.Join(destination, "catalog_alpha.json"), catalog, 0o644); err != nil {
		return "", 0, 0, err
	}
	if err := atomicWrite(filepath.Join(destination, "catalog_alpha.hash"), append([]byte(strings.ToLower(hash)), '\n'), 0o644); err != nil {
		return "", 0, 0, err
	}
	var total int64
	for index, relative := range paths {
		target := filepath.Join(destination, filepath.FromSlash(relative))
		if size, ok := validUnityBundle(target); ok {
			total += size
			continue
		}
		remote := base + "/" + escapePath(relative)
		size, err := downloadFile(ctx, options.Client, remote, target)
		if err != nil {
			return "", 0, 0, fmt.Errorf("resource fetch: bundle %q: %w", relative, err)
		}
		if _, ok := validUnityBundle(target); !ok {
			return "", 0, 0, fmt.Errorf("resource fetch: bundle %q lacks UnityFS signature", relative)
		}
		total += size
		if (index+1)%25 == 0 || index+1 == len(paths) {
			options.log(fmt.Sprintf("ServerData %d/%d (%.2f GiB)", index+1, len(paths), float64(total)/(1<<30)))
		}
	}
	return strings.ToLower(hash), len(paths), total, nil
}

func downloadBytes(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("resource fetch: GET %s: %w", rawURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resource fetch: GET %s: HTTP %s", rawURL, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("resource fetch: response exceeds %d bytes", limit)
	}
	return data, nil
}

func downloadFile(ctx context.Context, client *http.Client, rawURL, target string, expected ...release.ResourceFile) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %s", response.Status)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".bundle-*.part")
	if err != nil {
		return 0, err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	hash := sha256.New()
	var source io.Reader = response.Body
	if len(expected) != 0 {
		source = io.LimitReader(response.Body, expected[0].Size+1)
	}
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), source)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if syncErr != nil {
		return 0, syncErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if written == 0 {
		return 0, errors.New("empty response")
	}
	if len(expected) != 0 && (written != expected[0].Size || hex.EncodeToString(hash.Sum(nil)) != expected[0].SHA256) {
		return 0, errors.New("signed resource size or SHA-256 mismatch")
	}
	if err := replaceFile(temporaryPath, target); err != nil {
		return 0, err
	}
	return written, nil
}

func atomicWrite(target string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".resource-*.part")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFile(temporaryPath, target)
}

func replaceFile(source, target string) error {
	backup := target + ".replace-backup"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	moved := false
	if err := os.Rename(target, backup); err == nil {
		moved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		if moved {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if moved {
		_ = os.Remove(backup)
	}
	return nil
}

func validUnityBundle(path string) (int64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = file.Close() }()
	var magic [7]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil || string(magic[:]) != "UnityFS" {
		return 0, false
	}
	info, err := file.Stat()
	return info.Size(), err == nil && info.Size() > 7
}

func escapePath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func (o Options) log(message string) {
	if o.Progress != nil {
		o.Progress(message)
	}
}
