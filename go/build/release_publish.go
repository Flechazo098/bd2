package main

import (
	"archive/zip"
	"bd2server/internal/common/util"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	clientplugin "bd2server/internal/client/plugin"
	"bd2server/internal/common/i18n"
	releasepkg "bd2server/internal/common/release"
	"bd2server/internal/common/versionconfig"
)

// serverPlatforms are the packages published for self-updating servers.
var serverPlatforms = [][2]string{{"linux", "amd64"}, {"linux", "arm64"}}

func (t task) release(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		fmt.Println(`Usage:
  bd2w release changelog --component server|client [--bump patch|minor|major|none]
  bd2w release publish [-GameDir dir] [-SkipTests] [--output dir] [--image ref] [--allow-dirty] [--no-announcement] [--component all|schedules]

publish builds unsigned release materials described by versions.json and
CHANGELOG.md. Signing and uploading are independent publisher operations.`)
		return nil
	}
	switch args[0] {
	case "changelog":
		return t.releaseChangelog(args[1:])
	case "publish":
		return t.releasePublish(args[1:])
	}
	return fmt.Errorf("unknown release task %q", args[0])
}

var componentVersionLine = regexp.MustCompile(`("(server|client)_version"\s*:\s*")([0-9]+\.[0-9]+\.[0-9]+)\+(server|client)\.([0-9]+)\.([0-9]+)\.([0-9]+)(")`)

func (t task) releaseChangelog(args []string) error {
	fs := flag.NewFlagSet("release changelog", flag.ContinueOnError)
	component := fs.String("component", "", "server or client")
	bump := fs.String("bump", "patch", "patch, minor, major or none")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *component != releasepkg.ComponentServer && *component != releasepkg.ComponentClient {
		return errors.New("--component must be server or client")
	}
	versionsPath := filepath.Join(t.root, "versions.json")
	raw, err := os.ReadFile(versionsPath)
	if err != nil {
		return err
	}
	var version string
	updated := componentVersionLine.ReplaceAllStringFunc(string(raw), func(line string) string {
		m := componentVersionLine.FindStringSubmatch(line)
		if m[2] != *component {
			return line
		}
		var major, minor, patch int
		_, _ = fmt.Sscan(m[5], &major)
		_, _ = fmt.Sscan(m[6], &minor)
		_, _ = fmt.Sscan(m[7], &patch)
		switch *bump {
		case "major":
			major, minor, patch = major+1, 0, 0
		case "minor":
			minor, patch = minor+1, 0
		case "patch":
			patch++
		case "none":
		default:
			version = "!"
			return line
		}
		version = fmt.Sprintf("%s+%s.%d.%d.%d", m[3], m[4], major, minor, patch)
		return m[1] + version + m[8]
	})
	if version == "!" || version == "" {
		return fmt.Errorf("cannot bump %s_version with --bump %s", *component, *bump)
	}
	changelogPath := filepath.Join(t.root, "CHANGELOG.md")
	changelog, err := os.ReadFile(changelogPath)
	if err != nil {
		return err
	}
	entries, err := releasepkg.ParseChangelog(changelog)
	if err != nil {
		return fmt.Errorf("CHANGELOG.md: %w", err)
	}
	if _, exists := releasepkg.Find(entries, *component, version); exists {
		return fmt.Errorf("CHANGELOG.md already has %s %s", *component, version)
	}
	text := string(changelog)
	at := len(text)
	fence := ""
	offset := 0
	for line := range strings.SplitAfterSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		} else if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
		} else if strings.HasPrefix(line, "## server ") || strings.HasPrefix(line, "## client ") {
			at = offset
			break
		}
		offset += len(line)
	}
	section := releasepkg.ChangelogTemplate(*component, version, time.Now())
	text = text[:at] + section + "\n" + text[at:]
	if err := os.WriteFile(versionsPath, []byte(updated), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(changelogPath, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s_version is now %s; fill in its CHANGELOG.md section, then commit, push and prepare the release materials.\n", *component, version)
	return nil
}

type publishOptions struct {
	component  string
	build      buildOptions
	output     string
	image      string
	allowDirty bool
	silent     bool
}

func parsePublish(args []string) (publishOptions, error) {
	opts := publishOptions{component: "all"}
	var rest []string
	for i := 0; i < len(args); i++ {
		name, value, assigned := strings.Cut(args[i], "=")
		take := func() (string, error) {
			if assigned {
				return value, nil
			}
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			return args[i], nil
		}
		var err error
		switch name {
		case "--component":
			opts.component, err = take()
		case "--output":
			opts.output, err = take()
		case "--image":
			opts.image, err = take()
		case "--no-announcement":
			opts.silent = true
		case "--allow-dirty":
			opts.allowDirty = true
		default:
			rest = append(rest, args[i])
		}
		if err != nil {
			return opts, err
		}
	}
	if opts.component != "all" && opts.component != "schedules" {
		return opts, errors.New("--component must be all or schedules")
	}
	build, err := parseOptions(rest)
	opts.build = build
	return opts, err
}

func (t task) git(args ...string) (string, error) {
	command := exec.CommandContext(t.ctx, "git", args...)
	command.Dir = t.root
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// releaseCommit returns HEAD after checking that it is committed and pushed
// to both remotes, so every published release names a reachable commit.
func (t task) releaseCommit(allowDirty bool) (string, error) {
	commit, err := t.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if allowDirty {
		return commit, nil
	}
	if status, err := t.git("status", "--porcelain", "--untracked-files=normal"); err != nil {
		return "", err
	} else if status != "" {
		return "", errors.New("the working tree has uncommitted changes; commit and push before publishing")
	}
	for _, remote := range []string{"origin", "sighs"} {
		if _, err := t.git("fetch", "--quiet", remote, "main"); err != nil {
			return "", err
		}
		if _, err := t.git("merge-base", "--is-ancestor", "HEAD", remote+"/main"); err != nil {
			return "", fmt.Errorf("HEAD %s is not pushed to %s/main; push to both remotes first", commit[:12], remote)
		}
	}
	return commit, nil
}

func (t task) releasePublish(args []string) error {
	opts, err := parsePublish(args)
	if err != nil {
		return err
	}
	if opts.component == "schedules" {
		return t.publishSchedules(opts)
	}
	commit, err := t.releaseCommit(opts.allowDirty)
	if err != nil {
		return err
	}
	versions, err := versionconfig.Load(filepath.Join(t.root, "versions.json"))
	if err != nil {
		return err
	}
	fingerprint, err := releasepkg.LoadFingerprint(filepath.Join(t.root, releasepkg.FingerprintFileName), releasepkg.BuiltinChannel().TrustedKeys)
	if err != nil {
		return fmt.Errorf("release fingerprint must be generated independently before publication: %w", err)
	}
	if err := fingerprint.MatchesVersions(versions); err != nil {
		return err
	}
	changelog, err := os.ReadFile(filepath.Join(t.root, "CHANGELOG.md"))
	if err != nil {
		return err
	}
	entries, err := releasepkg.ParseChangelog(changelog)
	if err != nil {
		return fmt.Errorf("CHANGELOG.md: %w", err)
	}
	serverEntry, ok := releasepkg.Find(entries, releasepkg.ComponentServer, versions.ServerVersion)
	if !ok && !opts.silent {
		return fmt.Errorf("CHANGELOG.md has no section for server %s; run bd2w release changelog --component server", versions.ServerVersion)
	}
	clientEntry, ok := releasepkg.Find(entries, releasepkg.ComponentClient, versions.ClientVersion)
	if !ok && !opts.silent {
		return fmt.Errorf("CHANGELOG.md has no section for client %s; run bd2w release changelog --component client", versions.ClientVersion)
	}
	if opts.silent {
		serverEntry = releasepkg.Entry{Migration: releasepkg.MigrationNone}
		clientEntry = releasepkg.Entry{}
	}
	if err := t.build(opts.build); err != nil {
		return err
	}
	site := opts.output
	if site == "" {
		site = filepath.Join(t.root, ".build", "release-site")
	}
	if err := removeBuildOutput(filepath.Join(t.root, ".build"), site); err != nil {
		return err
	}
	publish := func(source, name, platform, id, version string) (releasepkg.File, error) {
		sum, err := util.SHA256File(source)
		if err != nil {
			return releasepkg.File{}, err
		}
		info, err := os.Stat(source)
		if err != nil {
			return releasepkg.File{}, err
		}
		relative := path.Join("files", sum[:16], name)
		if err := copyFile(source, filepath.Join(site, filepath.FromSlash(relative))); err != nil {
			return releasepkg.File{}, err
		}
		return releasepkg.File{Name: name, ID: id, Platform: platform, Version: version, Size: info.Size(), SHA256: sum, URL: "../" + relative}, nil
	}
	packageRoot := filepath.Join(t.root, ".build", "package")
	native := t.target.goos + "-" + t.target.goarch
	date := func(e releasepkg.Entry) string {
		if e.Date.IsZero() {
			return ""
		}
		return e.Date.Format("2006-01-02")
	}

	server := &releasepkg.Component{Version: versions.ServerVersion, Date: date(serverEntry), Notes: serverEntry.Notes(), Image: opts.image,
		Migration: &releasepkg.Migration{Kind: serverEntry.Migration, Backup: serverEntry.Backup}, MinimumPlugins: versions.MinimumPlugins.ByID()}
	delete(server.MinimumPlugins, "capture_environment")
	if serverEntry.Migration != releasepkg.MigrationNone {
		server.Migration.Note = i18n.Text{}
		for locale, notes := range serverEntry.Notes() {
			server.Migration.Note[locale] = firstLine(notes)
		}
	}
	for _, platform := range serverPlatforms {
		archive, err := t.serverPackage(versions, platform[0], platform[1], filepath.Join(packageRoot, "bd2server"))
		if err != nil {
			return err
		}
		f, err := publish(archive, filepath.Base(archive), platform[0]+"-"+platform[1], "", "")
		if err != nil {
			return err
		}
		server.Files = append(server.Files, f)
	}

	launcher := &releasepkg.Component{Version: versions.ClientVersion, Date: date(clientEntry), Notes: clientEntry.Notes()}
	clientPackage := filepath.Join(packageRoot, "bd2client")
	executable := "bd2client"
	if t.target.goos == "windows" {
		executable += ".exe"
	}
	for _, name := range []string{executable, "versions.json", "client-download.json", releasepkg.FingerprintFileName} {
		platform := ""
		if name == executable {
			platform = native
		}
		f, err := publish(filepath.Join(clientPackage, name), name, platform, "", "")
		if err != nil {
			return err
		}
		launcher.Files = append(launcher.Files, f)
	}

	plugins := &releasepkg.Component{Version: versions.ClientVersion, Date: date(clientEntry), Notes: clientEntry.Notes()}
	packagedPlugins, err := clientplugin.Packaged(clientPackage)
	if err != nil {
		return err
	}
	for _, artifact := range packagedPlugins {
		f, err := publish(artifact.Source, artifact.Name, "", artifact.ID, artifact.Version)
		if err != nil {
			return err
		}
		plugins.Files = append(plugins.Files, f)
	}

	schedules, err := t.stageSchedules(site, versions)
	if err != nil {
		return err
	}
	manifest := releasepkg.Manifest{Schema: releasepkg.SchemaVersion, Channel: releasepkg.BuiltinChannel().Name, PublishedAt: time.Now().UTC().Truncate(time.Second), Commit: commit,
		Launcher: launcher, Plugins: plugins, Server: server, Schedules: schedules, Fingerprint: fingerprint}
	if err := manifest.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(site, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(t.root, "CHANGELOG.md"), filepath.Join(site, "CHANGELOG.md")); err != nil {
		return err
	}
	fmt.Println("Unsigned release materials:", site)
	fmt.Println("Signing and publication must be performed independently by the publisher.")
	return nil
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}

// serverPackage cross-compiles the server for goos/goarch and zips it with
// the native package's data files. The executable keeps its 0755 mode.
func (t task) serverPackage(versions versionconfig.Config, goos, goarch, nativePackage string) (string, error) {
	work := filepath.Join(t.root, ".build", "release-server", goos+"-"+goarch)
	if err := removeBuildOutput(filepath.Join(t.root, ".build"), work); err != nil {
		return "", err
	}
	binary := filepath.Join(work, "bd2server")
	if goos == "windows" {
		binary += ".exe"
	}
	cross := t
	var err error
	if cross.target, err = platformTarget(goos, goarch); err != nil {
		return "", err
	}
	if err := cross.command("go", "build", "-tags", "release", "-trimpath", "-ldflags", "-s -w", "-o", binary, "./cmd/bd2server"); err != nil {
		return "", err
	}
	archive := filepath.Join(t.root, ".build", fmt.Sprintf("bd2server-%s-%s-%s.zip", versions.ServerVersion, goos, goarch))
	out, err := os.Create(archive)
	if err != nil {
		return "", err
	}
	writer := zip.NewWriter(out)
	add := func(source, name string, mode os.FileMode) error {
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		header := &zip.FileHeader{Name: "bd2server/" + name, Method: zip.Deflate}
		header.SetMode(mode)
		w, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, in)
		return err
	}
	err = add(binary, filepath.Base(binary), 0o755)
	if err == nil {
		err = filepath.WalkDir(nativePackage, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			name, err := filepath.Rel(nativePackage, file)
			if err != nil {
				return err
			}
			name = filepath.ToSlash(name)
			if strings.HasPrefix(name, "data/") || strings.TrimSuffix(name, ".exe") == "bd2server" {
				return nil
			}
			mode := os.FileMode(0o644)
			if strings.HasSuffix(name, ".sh") {
				mode = 0o755
			}
			return add(file, name, mode)
		})
	}
	if err = errors.Join(err, writer.Close(), out.Close()); err != nil {
		return "", err
	}
	return archive, os.RemoveAll(work)
}
