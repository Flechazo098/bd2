package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

type buildOptions struct {
	gameDir                  string
	skipTests, schedulesOnly bool
}

type releaseVersions struct {
	Game   string `json:"game_version"`
	Server string `json:"server_version"`
	Client string `json:"client_version"`
}

func parseOptions(args []string) (buildOptions, error) {
	var opts buildOptions
	for i := 0; i < len(args); i++ {
		name, value, assigned := strings.Cut(args[i], "=")
		switch strings.ToLower(name) {
		case "-gamedir", "--game-dir":
			if !assigned {
				i++
				if i >= len(args) {
					return opts, errors.New("-GameDir requires a directory")
				}
				value = args[i]
			}
			if strings.TrimSpace(value) == "" {
				return opts, errors.New("-GameDir requires a directory")
			}
			opts.gameDir = value
		case "-skiptests", "--skip-tests":
			if assigned {
				return opts, fmt.Errorf("unexpected value for %s", name)
			}
			opts.skipTests = true
		case "-schedulesonly", "--schedules-only":
			if assigned {
				return opts, fmt.Errorf("unexpected value for %s", name)
			}
			opts.schedulesOnly = true
		default:
			return opts, fmt.Errorf("unknown build option %q", args[i])
		}
	}
	return opts, nil
}
func (t task) versions() (releaseVersions, error) {
	v, err := commonconfig.Load(filepath.Join(t.root, "versions.json"), releaseVersions{}, commonconfig.DecodeOptions{RejectTrailing: true, AllowBOM: true})
	if err != nil {
		return v, err
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9.+_-]+$`)
	for _, s := range []string{v.Game, v.Server, v.Client} {
		if !valid.MatchString(s) {
			return v, fmt.Errorf("invalid release version %q", s)
		}
	}
	return v, nil
}

func (t task) schedules(gameVersion string) ([]string, string, error) {
	files, err := filepath.Glob(filepath.Join(t.root, "schedules", "*.bd2schedule"))
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", errors.New("no project schedule files found")
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(filepath.Base(files[i])) < strings.ToLower(filepath.Base(files[j]))
	})
	var lines []string
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, "", err
		}
		lines = append(lines, fmt.Sprintf("%s:%X", filepath.Base(file), sha256.Sum256(raw)))
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(lines, "\n"))))[:12]
	return files, "bd2schedules-" + gameVersion + "-" + hash, nil
}
