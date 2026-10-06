// Command build is the shared implementation behind the repository wrappers.
// It uses only the standard library and caches no game or account state.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

type target struct{ platform, goos, architecture, goarch, goarm string }

type task struct {
	root   string
	target target
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bd2w:", err)
		var child *exec.ExitError
		if errors.As(err, &child) && child.ExitCode() > 0 {
			os.Exit(child.ExitCode())
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Usage:
  bd2w build [-GameDir directory] [-SkipTests] [-SchedulesOnly]
  bd2w runClient [client options]
  bd2w runServer [server options]

Windows: .\bd2w build
Linux/macOS: ./bd2w build
Build automatically selects the native platform and architecture.
The wrappers compile go/build/build.go only when their cached executable is absent.`)
}

func run(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		usage()
		return nil
	}
	if args[0] != "build" && args[0] != "runClient" && args[0] != "runServer" {
		return fmt.Errorf("unknown task %q; use build, runClient or runServer", args[0])
	}
	if args[0] == "build" && len(args) == 2 && isHelp(args[1]) {
		usage()
		return nil
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	native, err := nativeTarget()
	if err != nil {
		return err
	}
	t := task{root, native}
	switch args[0] {
	case "runClient":
		return t.command("go", append([]string{"run", "-tags", "production", "./cmd/bd2client", "--dev", "run"}, args[1:]...)...)
	case "runServer":
		return t.command("go", append([]string{"run", "./cmd/bd2server", "--dev", "run"}, args[1:]...)...)
	default:
		opts, err := parseOptions(args[1:])
		if err != nil {
			return err
		}
		return t.build(opts)
	}
}

func isHelp(s string) bool {
	return s == "help" || s == "-h" || s == "--help" || strings.EqualFold(s, "-Help")
}

func repositoryRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	for dir := filepath.Dir(exe); ; dir = filepath.Dir(dir) {
		if regular(filepath.Join(dir, "versions.json")) && regular(filepath.Join(dir, "go", "go.mod")) {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return "", errors.New("repository root not found beside the cached build tool; launch through bd2w")
}

func nativeTarget() (target, error) {
	arch := runtime.GOARCH
	if runtime.GOOS == "windows" {
		machine := os.Getenv("PROCESSOR_ARCHITEW6432")
		if machine == "" {
			machine = os.Getenv("PROCESSOR_ARCHITECTURE")
		}
		switch strings.ToLower(machine) {
		case "amd64":
			arch = "amd64"
		case "arm64":
			arch = "arm64"
		case "x86":
			arch = "386"
		}
	} else {
		if out, err := exec.Command("uname", "-m").Output(); err == nil {
			arch = strings.TrimSpace(string(out))
		}
	}
	return platformTarget(runtime.GOOS, arch)
}

func platformTarget(goos, arch string) (target, error) {
	t := target{goos: goos}
	switch goos {
	case "windows", "linux":
		t.platform = goos
	case "darwin":
		t.platform = "macos"
	default:
		return t, fmt.Errorf("unsupported platform %q", goos)
	}
	switch strings.ToLower(arch) {
	case "amd64", "x86_64", "x64":
		t.architecture = "x64"
		t.goarch = "amd64"
	case "aarch64", "arm64":
		t.architecture = "arm64"
		t.goarch = "arm64"
	case "386", "i386", "i486", "i586", "i686", "x86":
		t.architecture = "x86"
		t.goarch = "386"
	case "arm", "armv7", "armv7l":
		t.architecture = "armv7"
		t.goarch = "arm"
		t.goarm = "7"
	default:
		return t, fmt.Errorf("unsupported architecture %q", arch)
	}
	return t, nil
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

// Every child receives a process-local native target and repository-local cache.
func (t task) command(program string, args ...string) error {
	cmd := exec.Command(program, args...)
	cmd.Dir = filepath.Join(t.root, "go")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = replaceEnvironment(os.Environ(), map[string]string{"GOCACHE": filepath.Join(t.root, "go", ".cache", "go-build"), "GOOS": t.target.goos, "GOARCH": t.target.goarch, "GOARM": t.target.goarm})
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", program, strings.Join(args, " "), err)
	}
	return nil
}

func replaceEnvironment(env []string, values map[string]string) []string {
	out := make([]string, 0, len(env)+len(values))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		remove := false
		for wanted := range values {
			if strings.EqualFold(key, wanted) {
				remove = true
				break
			}
		}
		if !remove {
			out = append(out, entry)
		}
	}
	for key, value := range values {
		if value != "" {
			out = append(out, key+"="+value)
		}
	}
	return out
}

func readJSON(path string, v any, strict bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})))
	if strict {
		d.DisallowUnknownFields()
	}
	if err = d.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%s must contain one JSON object", path)
	}
	return nil
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

func (t task) gameDirectory(explicit string) (string, string, string, error) {
	if explicit == "" {
		var c struct {
			SchemaVersion int    `json:"schema_version"`
			GameDirectory string `json:"game_directory"`
		}
		if err := readJSON(filepath.Join(t.root, "go", "config.json"), &c, true); err != nil {
			return "", "", "", fmt.Errorf("read go/config.json or pass -GameDir: %w", err)
		}
		if c.SchemaVersion != 1 || strings.TrimSpace(c.GameDirectory) == "" {
			return "", "", "", errors.New("go/config.json requires schema_version 1 and game_directory")
		}
		explicit = c.GameDirectory
	}
	game, err := filepath.Abs(explicit)
	if err != nil {
		return "", "", "", err
	}
	managed := filepath.Join(game, "BrownDust II_Data", "Managed")
	bep := filepath.Join(game, "BepInEx")
	if !regular(filepath.Join(managed, "Assembly-CSharp.dll")) {
		app := game
		if !strings.EqualFold(filepath.Ext(app), ".app") {
			app = filepath.Join(game, "BrownDust II.app")
		}
		managed = filepath.Join(app, "Contents", "Resources", "Data", "Managed")
		bep = filepath.Join(filepath.Dir(app), "BepInEx")
		inside := filepath.Join(app, "Contents", "BepInEx")
		if directory(inside) && !directory(bep) {
			bep = inside
		}
	}
	if !regular(filepath.Join(managed, "Assembly-CSharp.dll")) {
		return "", "", "", fmt.Errorf("Assembly-CSharp.dll missing in %s", game)
	}
	if !regular(filepath.Join(bep, "core", "BepInEx.dll")) {
		return "", "", "", fmt.Errorf("BepInEx/core/BepInEx.dll missing in %s", game)
	}
	return game, managed, bep, nil
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

func regular(path string) bool   { s, err := os.Stat(path); return err == nil && s.Mode().IsRegular() }
func directory(path string) bool { s, err := os.Stat(path); return err == nil && s.IsDir() }

func removeBuildOutput(root, path string) error {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove output outside .build: %s", path)
	}
	return os.RemoveAll(path)
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, writeErr := io.Copy(out, in)
	return errors.Join(writeErr, out.Close())
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		out := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(out, 0755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected seed symlink: %s", path)
		}
		return copyFile(path, out)
	})
}

func archiveDirectory(source, destination string) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".bd2-archive-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	archive := zip.NewWriter(file)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected package symlink: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(source), path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if entry.IsDir() {
			header.Name += "/"
			_, err = archive.CreateHeader(header)
			return err
		}
		header.Method = zip.Deflate
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(writer, in)
		return errors.Join(writeErr, in.Close())
	})
	err = errors.Join(err, archive.Close(), file.Close())
	if err != nil {
		return err
	}
	return os.Rename(temp, destination)
}
