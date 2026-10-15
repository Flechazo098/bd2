package release

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`^(?:([0-9]+\.[0-9]+\.[0-9]+)\+(client|server)\.)?([0-9]+\.[0-9]+\.[0-9]+)$`)

// Version is either a plain X.Y.Z plugin version or a component release such
// as 2.36.7+server.0.1.0, where the game version is compared first.
type Version struct {
	Game      [3]int
	HasGame   bool
	Kind      string
	Component [3]int
	raw       string
}

func ParseVersion(raw string) (Version, error) {
	match := versionPattern.FindStringSubmatch(raw)
	if match == nil {
		return Version{}, fmt.Errorf("release: invalid version %q", raw)
	}
	v := Version{Kind: match[2], raw: raw}
	if match[1] != "" {
		v.HasGame = true
		v.Game = triple(match[1])
	}
	v.Component = triple(match[3])
	return v, nil
}

func triple(raw string) [3]int {
	var out [3]int
	for i, part := range strings.SplitN(raw, ".", 3) {
		out[i], _ = strconv.Atoi(part)
	}
	return out
}

func (v Version) String() string { return v.raw }

// Short drops the game prefix: 2.36.7+server.0.1.0 displays as 0.1.0.
func (v Version) Short() string {
	if !v.HasGame {
		return v.raw
	}
	return fmt.Sprintf("%d.%d.%d", v.Component[0], v.Component[1], v.Component[2])
}

// GameVersion returns the X.Y.Z game prefix, or "" for plain versions.
func (v Version) GameVersion() string {
	if !v.HasGame {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v.Game[0], v.Game[1], v.Game[2])
}

// Compare orders versions of the same kind; a game update outranks any
// component number.
func (v Version) Compare(other Version) int {
	if c := compareTriple(v.Game, other.Game); c != 0 {
		return c
	}
	return compareTriple(v.Component, other.Component)
}

func compareTriple(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Newer reports whether candidate is a strictly newer release than current.
// Both must parse and be of the same kind.
func Newer(current, candidate string) (bool, error) {
	a, err := ParseVersion(current)
	if err != nil {
		return false, err
	}
	b, err := ParseVersion(candidate)
	if err != nil {
		return false, err
	}
	if a.Kind != b.Kind || a.HasGame != b.HasGame {
		return false, fmt.Errorf("release: cannot compare %q with %q", current, candidate)
	}
	return b.Compare(a) > 0, nil
}
