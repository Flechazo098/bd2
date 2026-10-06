package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	var v releaseVersions
	if err := readJSON(filepath.Join(t.root, "versions.json"), &v, false); err != nil {
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
func (t task) build(opts buildOptions) error {
	v, err := t.versions()
	if err != nil {
		return err
	}
	schedules, scheduleName, err := t.schedules(v.Game)
	if err != nil {
		return err
	}
	buildRoot := filepath.Join(t.root, ".build")
	if opts.schedulesOnly {
		out := filepath.Join(buildRoot, scheduleName)
		if err := os.MkdirAll(out, 0755); err != nil {
			return err
		}
		for _, file := range schedules {
			if err := copyFile(file, filepath.Join(out, filepath.Base(file))); err != nil {
				return err
			}
		}
		fmt.Println("Built schedule release:", out)
		return nil
	}
	game, managed, bep, err := t.gameDirectory(opts.gameDir)
	if err != nil {
		return err
	}
	packageRoot := filepath.Join(buildRoot, "package")
	server := filepath.Join(packageRoot, "bd2server")
	client := filepath.Join(packageRoot, "bd2client")
	suffix := "-" + t.target.platform + "-" + t.target.architecture + ".zip"
	serverZip := filepath.Join(buildRoot, "bd2server-"+v.Server+suffix)
	clientZip := filepath.Join(buildRoot, "bd2client-"+v.Client+suffix)
	if err := os.MkdirAll(buildRoot, 0755); err != nil {
		return err
	}
	for _, path := range []string{packageRoot, serverZip, clientZip} {
		if err := removeBuildOutput(buildRoot, path); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Join(server, "go"), filepath.Join(server, "data", "state"), filepath.Join(server, "schedules"), filepath.Join(client, "plugins")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}
	if !opts.skipTests {
		for _, args := range [][]string{{"test", "./..."}, {"vet", "./..."}, {"test", "-tags", "release,production", "./..."}, {"vet", "-tags", "release,production", "./..."}} {
			if err := t.command("go", args...); err != nil {
				return err
			}
		}
	}
	ext := ""
	clientFlags := "-s -w"
	if t.target.goos == "windows" {
		ext = ".exe"
		clientFlags = "-H windowsgui -s -w"
	}
	if err := t.command("go", "build", "-tags", "release", "-trimpath", "-ldflags", "-s -w", "-o", filepath.Join(server, "bd2server"+ext), "./cmd/bd2server"); err != nil {
		return err
	}
	if err := t.command("go", "build", "-tags", "release,production", "-trimpath", "-ldflags", clientFlags, "-o", filepath.Join(client, "bd2client"+ext), "./cmd/bd2client"); err != nil {
		return err
	}
	var shared []byte
	for _, plugin := range []string{"LocalIdentity", "LoginUI", "CashShop"} {
		project := filepath.Join(t.root, "plugins", plugin, plugin+".csproj")
		if err := t.command("dotnet", "build", project, "-c", "Release", "-p:GameDir="+game, "-p:BD2ManagedDir="+managed, "-p:BD2BepInExDir="+bep, "--nologo"); err != nil {
			return err
		}
		out := filepath.Join(t.root, "plugins", plugin, "bin", "Release", "netstandard2.1")
		raw, err := os.ReadFile(filepath.Join(out, "BD2.GameNames.dll"))
		if err != nil {
			return err
		}
		if shared != nil && !bytes.Equal(shared, raw) {
			return errors.New("plugins were built with different BD2.GameNames libraries")
		}
		shared = raw
		for _, name := range []string{"BD2" + plugin + ".dll", "BD2.GameNames.dll"} {
			if err := copyFile(filepath.Join(out, name), filepath.Join(client, "plugins", name)); err != nil {
				return err
			}
		}
	}
	if err := copyTree(filepath.Join(t.root, "go", "seed"), filepath.Join(server, "go", "seed")); err != nil {
		return err
	}
	for _, file := range schedules {
		if err := copyFile(file, filepath.Join(server, "schedules", filepath.Base(file))); err != nil {
			return err
		}
	}
	for _, name := range []string{"versions.json", "README.md", "LICENSE"} {
		for _, out := range []string{server, client} {
			if err := copyFile(filepath.Join(t.root, name), filepath.Join(out, name)); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"AUTHENTICATION.md", "RESOURCES.md", "GAME_CONFIGURATION.md"} {
		if err := copyFile(filepath.Join(t.root, name), filepath.Join(server, name)); err != nil {
			return err
		}
	}
	if err := archiveDirectory(server, serverZip); err != nil {
		return err
	}
	if err := archiveDirectory(client, clientZip); err != nil {
		return err
	}
	fmt.Println("Built server archive:", serverZip)
	fmt.Println("Built client archive:", clientZip)
	return nil
}
