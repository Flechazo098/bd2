package release

import (
	"archive/zip"
	"bd2server/internal/common/util"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxManifestBytes  = 32 << 20
	maxChangelogBytes = 2 << 20
)

// DefaultClient bounds connection setup; downloads are bounded by context.
var DefaultClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	IdleConnTimeout:       60 * time.Second,
}}

func get(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	if client == nil {
		client = DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Cache-Control", "no-cache")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", request.URL.Redacted(), response.StatusCode)
	}
	return response, nil
}

func readLimited(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	response, err := get(ctx, client, address)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", address, limit)
	}
	return raw, nil
}

// FetchManifest downloads and verifies the signed manifest at address.
func FetchManifest(ctx context.Context, client *http.Client, address string, keys []TrustedKey) (Manifest, []byte, error) {
	if address == "" {
		return Manifest{}, nil, errors.New("release: no manifest URL is configured")
	}
	if _, err := Resolve(address, address); err != nil {
		return Manifest{}, nil, err
	}
	raw, err := readLimited(ctx, client, address, maxManifestBytes)
	if err != nil {
		return Manifest{}, nil, err
	}
	manifest, err := Verify(raw, keys)
	if err != nil {
		return Manifest{}, nil, err
	}
	if manifest.Channel != BuiltinChannel().Name {
		return Manifest{}, nil, errors.New("release: manifest belongs to a different release channel")
	}
	return manifest, raw, nil
}

// Progress receives the bytes written so far for one file.
type Progress func(name string, done, total int64)

// Download fetches file into directory unless a verified copy already exists,
// and returns its path. The file appears under its final name only after its
// size and SHA-256 matched, so a partial download is never mistaken for one.
func Download(ctx context.Context, client *http.Client, manifestURL string, file File, directory string, progress Progress) (string, error) {
	target := filepath.Join(directory, file.Name)
	if Matches(target, file) {
		return target, nil
	}
	address, err := Resolve(manifestURL, file.URL)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	response, err := get(ctx, client, address)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	temporary, err := os.CreateTemp(directory, "."+file.Name+"-*.part")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	digest := sha256.New()
	writer := io.MultiWriter(temporary, digest)
	var written int64
	buffer := make([]byte, 256<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if written+int64(n) > file.Size {
				_ = temporary.Close()
				return "", fmt.Errorf("release: %s is larger than the manifest states", file.Name)
			}
			if _, err := writer.Write(buffer[:n]); err != nil {
				_ = temporary.Close()
				return "", err
			}
			written += int64(n)
			if progress != nil {
				progress(file.Name, written, file.Size)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = temporary.Close()
			return "", readErr
		}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if written != file.Size || hex.EncodeToString(digest.Sum(nil)) != file.SHA256 {
		return "", fmt.Errorf("release: %s failed verification; the download was discarded", file.Name)
	}
	if err := util.ReplaceFile(temporary.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

// Matches reports whether path exists with file's size and digest.
func Matches(path string, file File) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
		return false
	}
	sum, err := util.SHA256File(path)
	return err == nil && sum == file.SHA256
}

// Extract unpacks a verified zip archive into an empty directory, refusing
// entries that would escape it.
func Extract(archive, directory string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	root, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	for _, entry := range reader.File {
		name := filepath.FromSlash(entry.Name)
		target := filepath.Join(root, name)
		if !strings.HasPrefix(target, root+string(filepath.Separator)) || filepath.IsAbs(name) {
			return fmt.Errorf("release: archive entry %q escapes the target", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("release: archive entry %q is not a regular file", entry.Name)
		}
		if err := extractFile(entry, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(entry *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	mode := os.FileMode(0o644)
	if entry.Mode()&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, io.LimitReader(source, int64(entry.UncompressedSize64)+1)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// FetchSigned returns a changelog and its detached signature from the first
// mirror that serves both.
func FetchSigned(ctx context.Context, client *http.Client, addresses []string, keys []TrustedKey) ([]byte, []byte, string, error) {
	var failures []error
	for _, address := range addresses {
		if _, err := Resolve(address, address); err != nil {
			failures = append(failures, err)
			continue
		}
		raw, err := readLimited(ctx, client, address, maxChangelogBytes)
		if err == nil {
			var signature []byte
			if signature, err = readLimited(ctx, client, address+".sig", 4<<10); err == nil {
				if err = VerifyChangelog(raw, signature, keys); err == nil {
					return raw, signature, address, nil
				}
			}
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return nil, nil, "", errors.New("release: no changelog source configured")
	}
	return nil, nil, "", errors.Join(failures...)
}
