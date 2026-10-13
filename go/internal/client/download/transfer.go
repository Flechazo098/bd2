package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	commonconfig "bd2server/internal/common/config"
)

type file struct {
	remote, target, digest string
	size                   int64
	bundle                 bool
}

type receipt struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type downloader struct {
	root, checkpoint string
	client           *http.Client
	out              io.Writer
	mu               sync.Mutex
	records          map[string]receipt
}

func newDownloader(root string, out io.Writer) (*downloader, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConnsPerHost = 16
	transport.ResponseHeaderTimeout = 30 * time.Second
	d := &downloader{root: root, checkpoint: filepath.Join(root, "downloads", "verified-downloads.json"), client: &http.Client{Transport: transport, Timeout: 30 * time.Minute}, out: out, records: map[string]receipt{}}
	saved, err := commonconfig.Load(d.checkpoint, struct {
		SchemaVersion int                `json:"schema_version"`
		Files         map[string]receipt `json:"files"`
	}{}, commonconfig.DecodeOptions{AllowBOM: true, RejectTrailing: true, RejectNull: true})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if saved.SchemaVersion != 1 {
			return nil, errors.New("unsupported download checkpoint")
		}
		if saved.Files != nil {
			d.records = saved.Files
		}
	}
	return d, nil
}

func (d *downloader) log(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = fmt.Fprintf(d.out, format+"\n", args...)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (d *downloader) verified(item file) bool {
	info, err := os.Stat(item.target)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || (item.size > 0 && item.size != info.Size()) {
		return false
	}
	if item.bundle && !validBundle(item.target, info.Size()) {
		return false
	}
	key, err := filepath.Rel(d.root, item.target)
	if err != nil {
		return false
	}
	d.mu.Lock()
	previous := d.records[filepath.ToSlash(key)]
	d.mu.Unlock()
	expected := item.digest
	if expected == "" && previous.Bytes == info.Size() {
		expected = previous.SHA256
	}
	if expected == "" {
		return false
	}
	actual, err := hashFile(item.target)
	return err == nil && actual == expected
}

func (d *downloader) receive(ctx context.Context, item file, origins []string) error {
	if d.verified(item) {
		return nil
	}
	if len(origins) == 0 {
		return errors.New("no download origins")
	}
	var last error
	for attempt := 0; attempt < max(4, len(origins)*2); attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = d.receiveOnce(ctx, item, origins[attempt%len(origins)])
		if last == nil {
			return nil
		}
		d.log("Retry %s: %v", filepath.Base(item.target), last)
	}
	return fmt.Errorf("download %s: %w", item.remote, last)
}

func (d *downloader) receiveOnce(ctx context.Context, item file, origin string) error {
	if err := os.MkdirAll(filepath.Dir(item.target), 0755); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(origin, "/")+item.remote, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := d.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("full GET requires HTTP 200, got %s", response.Status)
	}
	temporary, err := os.CreateTemp(filepath.Dir(item.target), ".bd2-download-*.part")
	if err != nil {
		return err
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, h), response.Body)
	if err != nil {
		return err
	}
	if written == 0 || (response.ContentLength >= 0 && written != response.ContentLength) || (item.size > 0 && written != item.size) {
		return errors.New("incomplete download length")
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if item.digest != "" && item.digest != actual {
		return errors.New("SHA-256 mismatch")
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if item.bundle && !validBundle(temporary.Name(), written) {
		return errors.New("invalid UnityFS bundle length")
	}
	if err := os.Rename(temporary.Name(), item.target); err != nil {
		return err
	}
	key, err := filepath.Rel(d.root, item.target)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records[filepath.ToSlash(key)] = receipt{Bytes: written, SHA256: actual}
	return commonconfig.Save(d.checkpoint, struct {
		SchemaVersion int                `json:"schema_version"`
		Files         map[string]receipt `json:"files"`
	}{1, d.records}, commonconfig.WriteOptions{FileMode: 0644})
}

func (d *downloader) receiveAll(ctx context.Context, files []file, origins []string, workers int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan file)
	var wg sync.WaitGroup
	var first error
	var errOnce sync.Once
	completed := 0
	var progress sync.Mutex
	for range min(workers, len(files)) {
		wg.Go(func() {
			for item := range jobs {
				if err := d.receive(ctx, item, origins); err != nil {
					errOnce.Do(func() { first = err; cancel() })
					return
				}
				progress.Lock()
				completed++
				if completed%25 == 0 || completed == len(files) {
					d.log("Completed %d/%d", completed, len(files))
				}
				progress.Unlock()
			}
		})
	}
feed:
	for _, item := range files {
		select {
		case jobs <- item:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

func (d *downloader) fastest(ctx context.Context, origins []string, relative, label string) ([]string, error) {
	type result struct {
		origin string
		speed  float64
		err    error
	}
	results := make([]result, len(origins))
	var wg sync.WaitGroup
	for i, origin := range origins {
		wg.Go(func() {
			probe, cancel := context.WithTimeout(ctx, 18*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(probe, http.MethodGet, strings.TrimRight(origin, "/")+relative, nil)
			if err != nil {
				results[i] = result{origin: origin, err: err}
				return
			}
			request.Header.Set("Accept-Encoding", "identity")
			start := time.Now()
			response, err := d.client.Do(request)
			if err != nil {
				results[i] = result{origin: origin, err: err}
				return
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				results[i] = result{origin: origin, err: fmt.Errorf("HTTP %s", response.Status)}
				return
			}
			n, err := io.Copy(io.Discard, io.LimitReader(response.Body, 4<<20))
			speed := float64(0)
			if n >= 256<<10 {
				speed = float64(n) / time.Since(start).Seconds()
			}
			results[i] = result{origin: origin, speed: speed, err: err}
		})
	}
	wg.Wait()
	slices.SortStableFunc(results, func(a, b result) int {
		if a.speed > b.speed {
			return -1
		}
		if a.speed < b.speed {
			return 1
		}
		return 0
	})
	var ranked []string
	for _, r := range results {
		if r.speed > 0 {
			ranked = append(ranked, r.origin)
			d.log("%s %s: %.2f MiB/s", label, r.origin, r.speed/(1<<20))
		} else {
			d.log("%s %s: unavailable (%v)", label, r.origin, r.err)
		}
	}
	if len(ranked) == 0 {
		return nil, fmt.Errorf("%s: all download origins unavailable", label)
	}
	d.log("%s selected: %s", label, ranked[0])
	return ranked, nil
}

func escapedPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bd2-download-*.part")
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
