package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type sdkOptions struct{ gameDir, versionConfig, packageVersion, outputDirectory, packageDirectory, gameMapping string }

func (t task) sdkOptions(args []string, mode string) (sdkOptions, error) {
	var o sdkOptions
	fs := flag.NewFlagSet("sdk "+mode, flag.ContinueOnError)
	fs.StringVar(&o.gameDir, "game-dir", "", "game installation directory")
	fs.StringVar(&o.versionConfig, "version-config", filepath.Join(t.root, "versions.json"), "version configuration")
	switch mode {
	case "pack":
		fs.StringVar(&o.packageVersion, "package-version", "", "version-locked NuGet version")
		fs.StringVar(&o.outputDirectory, "output-directory", filepath.Join(t.root, ".build", "nuget"), "package output directory")
	case "verify":
		fs.StringVar(&o.packageVersion, "package-version", "", "version-locked NuGet version")
		fs.StringVar(&o.packageDirectory, "package-directory", filepath.Join(t.root, ".build", "nuget"), "local package feed")
	case "update-names":
		fs.StringVar(&o.gameMapping, "game-mapping", "", "official obfuscation mapping")
	default:
		return o, fmt.Errorf("unknown SDK mode %q", mode)
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("unexpected SDK argument %q", fs.Arg(0))
	}
	var err error
	for _, p := range []*string{&o.versionConfig, &o.outputDirectory, &o.packageDirectory} {
		if *p == "" {
			continue
		}
		*p, err = filepath.Abs(*p)
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

func (t task) sdkPackageVersion(o sdkOptions) (string, string, error) {
	var versions releaseVersions
	if err := readJSON(o.versionConfig, &versions, false); err != nil {
		return "", "", err
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(versions.Game) {
		return "", "", fmt.Errorf("invalid game version %q", versions.Game)
	}
	version := o.packageVersion
	if version == "" {
		raw, err := os.ReadFile(filepath.Join(t.root, "plugins", "PackageMetadata.props"))
		if err != nil {
			return "", "", err
		}
		var metadata struct {
			Groups []struct {
				Version string `xml:"BD2PackageVersion"`
			} `xml:"PropertyGroup"`
		}
		if err = xml.Unmarshal(raw, &metadata); err != nil {
			return "", "", err
		}
		for _, g := range metadata.Groups {
			if g.Version != "" {
				if version != "" {
					return "", "", errors.New("duplicate BD2PackageVersion")
				}
				version = strings.TrimSpace(g.Version)
			}
		}
		if version == "" {
			return "", "", errors.New("PackageMetadata.props requires BD2PackageVersion")
		}
		version += "-game." + versions.Game
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+-game\.\d+\.\d+\.\d+(?:\.[A-Za-z0-9-]+)*$`).MatchString(version) {
		return "", "", fmt.Errorf("invalid SDK package version %q", version)
	}
	if !strings.Contains(version, "-game."+versions.Game+".") && !strings.HasSuffix(version, "-game."+versions.Game) {
		return "", "", errors.New("package game suffix must match version configuration")
	}
	return version, versions.Game, nil
}

func (t task) sdkCommand(workdir string, env map[string]string, capture bool, args ...string) (string, error) {
	cmd := exec.Command("dotnet", args...)
	cmd.Dir = workdir
	cmd.Stdin = os.Stdin
	cmd.Env = replaceEnvironment(os.Environ(), env)
	if capture {
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("dotnet %s: %w", strings.Join(args, " "), err)
	}
	return "", nil
}

func (t task) sdkWorkspace(prefix string) (string, error) {
	build := filepath.Join(t.root, ".build")
	if err := os.MkdirAll(build, 0755); err != nil {
		return "", err
	}
	return os.MkdirTemp(build, prefix)
}
func (t task) sdkCleanup(path string, result *error) {
	if err := removeBuildOutput(filepath.Join(t.root, ".build"), path); err != nil {
		*result = errors.Join(*result, err)
	}
}
func sdkReadTrimmed(path string) (string, error) {
	raw, err := os.ReadFile(path)
	return strings.TrimSpace(string(raw)), err
}
func sdkXML(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}
func sdkNugetConfig(path, feed, cache string) error {
	config := `<configuration><packageSources><clear/><add key="bd2-local" value="` + sdkXML(feed) + `"/><add key="nuget.org" value="https://api.nuget.org/v3/index.json"/></packageSources>`
	if cache != "" {
		config += `<config><add key="globalPackagesFolder" value="` + sdkXML(cache) + `"/></config>`
	}
	config += `</configuration>`
	return os.WriteFile(path, []byte(config), 0644)
}
func sdkDirectorySize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}
func sdkAtomicCopy(source, destination string) (result error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.CreateTemp(filepath.Dir(destination), ".names-*.gz")
	if err != nil {
		return err
	}
	temp := out.Name()
	defer func() { _ = out.Close(); _ = os.Remove(temp) }()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(temp, destination)
}
