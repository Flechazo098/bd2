// Package release defines the signed release manifest shared by the build
// tool, the launcher and the server: what each component's current version is,
// where its files are, their sizes and SHA-256 digests, and what an install
// requires. Components are compared and downloaded independently.
package release

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"bd2server/internal/common/i18n"
)

const SchemaVersion = 1

// Migration kinds a server release may declare.
const (
	MigrationNone         = "none"
	MigrationReversible   = "reversible"
	MigrationIrreversible = "irreversible"
)

type Manifest struct {
	Schema      int          `json:"schema"`
	Channel     string       `json:"channel"`
	PublishedAt time.Time    `json:"published_at"`
	Commit      string       `json:"commit"`
	Launcher    *Component   `json:"launcher,omitempty"`
	Plugins     *Component   `json:"plugins,omitempty"`
	Server      *Component   `json:"server,omitempty"`
	Schedules   *Schedules   `json:"schedules,omitempty"`
	Fingerprint *Fingerprint `json:"fingerprint,omitempty"`
}

type Component struct {
	Version string    `json:"version"`
	Date    string    `json:"date"`
	Notes   i18n.Text `json:"notes"`
	Files   []File    `json:"files"`
	// Server-only fields.
	Image          string            `json:"image,omitempty"`
	Migration      *Migration        `json:"migration,omitempty"`
	MinimumPlugins map[string]string `json:"minimum_plugins,omitempty"`
}

// File is one downloadable unit. Platform is GOOS-GOARCH, or empty for files
// that are identical on every platform (plugins). Version is set for plugin
// DLLs that carry their own number.
type File struct {
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	Platform string `json:"platform,omitempty"`
	Version  string `json:"version,omitempty"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	URL      string `json:"url"`
}

type Migration struct {
	Kind   string    `json:"kind"`
	Backup bool      `json:"backup"`
	Note   i18n.Text `json:"note,omitempty"`
}

var (
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

func (m Manifest) Validate() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf("release: unsupported manifest schema %d", m.Schema)
	}
	if m.Channel == "" || m.PublishedAt.IsZero() {
		return errors.New("release: manifest needs a channel and publication time")
	}
	if !commitPattern.MatchString(m.Commit) {
		return errors.New("release: manifest commit must be a full Git commit hash")
	}
	if m.Launcher == nil && m.Plugins == nil && m.Server == nil {
		return errors.New("release: manifest has no components")
	}
	for name, c := range map[string]*Component{"launcher": m.Launcher, "plugins": m.Plugins, "server": m.Server} {
		if c == nil {
			continue
		}
		if err := c.validate(name); err != nil {
			return err
		}
	}
	if m.Schedules != nil {
		if err := m.Schedules.Validate(); err != nil {
			return err
		}
	}
	if m.Fingerprint != nil {
		if err := m.Fingerprint.Validate(); err != nil {
			return err
		}
		if m.Server != nil && m.Server.Version != m.Fingerprint.ServerVersion {
			return errors.New("release: server and fingerprint versions differ")
		}
		if m.Launcher != nil && m.Launcher.Version != m.Fingerprint.ClientVersion {
			return errors.New("release: launcher and fingerprint versions differ")
		}
	}
	return nil
}

func (c *Component) validate(name string) error {
	kind := map[string]string{"launcher": "client", "plugins": "client", "server": "server"}[name]
	v, err := ParseVersion(c.Version)
	if err != nil || v.Kind != kind {
		return fmt.Errorf("release: %s version %q must be a %s release version", name, c.Version, kind)
	}
	if len(c.Files) == 0 && c.Image == "" {
		return fmt.Errorf("release: %s has nothing to download", name)
	}
	seen := map[string]bool{}
	for _, f := range c.Files {
		if f.Name == "" || path.Base(f.Name) != f.Name || strings.ContainsAny(f.Name, `\:`) {
			return fmt.Errorf("release: %s file name %q is invalid", name, f.Name)
		}
		key := f.Platform + "/" + f.Name
		if seen[key] {
			return fmt.Errorf("release: %s lists %s twice", name, key)
		}
		seen[key] = true
		if f.Size <= 0 || !digestPattern.MatchString(f.SHA256) || f.URL == "" {
			return fmt.Errorf("release: %s file %s needs a size, a lowercase SHA-256 and a URL", name, f.Name)
		}
		if f.Version != "" {
			if _, err := ParseVersion(f.Version); err != nil {
				return err
			}
		}
	}
	if name == "server" {
		if c.Migration == nil || !slices.Contains([]string{MigrationNone, MigrationReversible, MigrationIrreversible}, c.Migration.Kind) {
			return errors.New("release: server must declare its migration kind")
		}
		for id, minimum := range c.MinimumPlugins {
			if _, err := ParseVersion(minimum); err != nil || id == "" {
				return fmt.Errorf("release: invalid minimum plugin %s=%q", id, minimum)
			}
		}
	}
	return nil
}

// FilesFor returns the files a host of platform needs: platform-specific files
// plus the shared ones.
func (c *Component) FilesFor(platform string) []File {
	var out []File
	for _, f := range c.Files {
		if f.Platform == "" || f.Platform == platform {
			out = append(out, f)
		}
	}
	return out
}

// Resolve turns a file URL into an absolute HTTPS URL; relative URLs are
// relative to the manifest, so moving files to a CDN only changes the base.
func Resolve(manifestURL, fileURL string) (string, error) {
	base, err := url.Parse(manifestURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(fileURL)
	if err != nil {
		return "", err
	}
	out := base.ResolveReference(ref)
	if out.Scheme != "https" && !loopback(out.Hostname()) {
		return "", fmt.Errorf("release: %s must use HTTPS", out.Redacted())
	}
	return out.String(), nil
}

func loopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
