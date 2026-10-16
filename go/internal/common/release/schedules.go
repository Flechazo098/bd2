package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"

	"bd2server/internal/common/i18n"
)

// Schedules is the schedule component of the release manifest.
// ID identifies content and compatibility; it is independent of program versions.
type Schedules struct {
	ID              string    `json:"id"`
	GameVersion     string    `json:"game_version"`
	GameDataVersion string    `json:"game_data_version"`
	Notes           i18n.Text `json:"notes,omitempty"`
	Files           []File    `json:"files"`
}

func (s Schedules) ContentID() string {
	files := slices.Clone(s.Files)
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Name, b.Name) })
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "bd2-schedules-v1\n%s\n%s\n", s.GameVersion, s.GameDataVersion)
	for _, f := range files {
		_, _ = fmt.Fprintf(h, "%s\n%d\n%s\n", f.Name, f.Size, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s Schedules) Validate() error {
	if strings.TrimSpace(s.GameVersion) == "" || strings.TrimSpace(s.GameDataVersion) == "" || len(s.Files) == 0 || len(s.Files) > 128 {
		return fmt.Errorf("release: schedules need compatible game and GameData versions and 1 to 128 files")
	}
	seen := map[string]bool{}
	for _, f := range s.Files {
		name := strings.ToLower(f.Name)
		if f.Name == "" || path.Base(f.Name) != f.Name || strings.ContainsAny(f.Name, "\\:\r\n") || !strings.HasSuffix(f.Name, ".bd2schedule") || strings.HasSuffix(strings.TrimSuffix(f.Name, ".bd2schedule"), ".") || seen[name] {
			return fmt.Errorf("release: invalid or duplicate schedule filename %q", f.Name)
		}
		seen[name] = true
		if f.Platform != "" || f.ID != "" || f.Version != "" || f.Size <= 0 || f.Size > 8*1024*1024 || !digestPattern.MatchString(f.SHA256) || f.URL == "" {
			return fmt.Errorf("release: invalid schedule file %q", f.Name)
		}
	}
	if !digestPattern.MatchString(s.ID) || s.ID != s.ContentID() {
		return fmt.Errorf("release: schedules content ID does not match its file set")
	}
	return nil
}
