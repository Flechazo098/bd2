// Package instance prepares independent local clients for multiplayer testing.
package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	clientlayout "bd2server/internal/client/layout"
)

type Instance struct {
	ID            string
	Root          string
	GameDirectory string
	Created       bool
}

type manifest struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Source        string `json:"source"`
	GameVersion   string `json:"game_version"`
}

func Prepare(name, source, version string) (Instance, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || len(name) > 32 || name == "default" {
		return Instance{}, errors.New("instance name must be 1-32 letters, digits, underscores or hyphens; default is reserved")
	}
	for _, ch := range name {
		valid := ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-'
		if !valid {
			return Instance{}, errors.New("instance name may contain only letters, digits, underscores and hyphens")
		}
	}
	installation, err := clientlayout.Resolve(source)
	if err != nil {
		return Instance{}, err
	}
	parent := filepath.Join(filepath.Dir(installation.Root), "BD2 Instances")
	if filepath.Base(filepath.Dir(installation.Root)) == "game" || filepath.Base(filepath.Dir(filepath.Dir(installation.Root))) == "BD2 Instances" {
		return Instance{}, errors.New("select the original installation when preparing another client instance")
	}
	result := Instance{ID: name, Root: filepath.Join(parent, name)}
	result.GameDirectory = filepath.Join(result.Root, "game")
	if installation.Kind == clientlayout.MacOS {
		result.GameDirectory = filepath.Join(result.GameDirectory, filepath.Base(installation.Root))
	}
	want := manifest{SchemaVersion: 1, ID: name, Source: installation.Root, GameVersion: version}
	data, err := os.ReadFile(filepath.Join(result.Root, "instance.json"))
	if err == nil {
		var saved manifest
		if err := json.Unmarshal(data, &saved); err != nil || saved != want {
			return Instance{}, errors.New("client instance belongs to a different installation or game version; choose a new instance name")
		}
		if _, err := clientlayout.Resolve(result.GameDirectory); err != nil {
			return Instance{}, fmt.Errorf("client instance is incomplete: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Instance{}, err
	}
	if _, err := os.Stat(result.Root); err == nil {
		return Instance{}, errors.New("instance directory already exists without its manifest; choose another name")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Instance{}, err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Instance{}, err
	}
	staged, err := os.MkdirTemp(parent, "."+name+"-preparing-")
	if err != nil {
		return Instance{}, err
	}
	defer func() {
		if pathWithin(parent, staged) && strings.HasPrefix(filepath.Base(staged), "."+name+"-preparing-") {
			_ = os.RemoveAll(staged)
		}
	}()
	target := filepath.Join(staged, "game")
	if installation.Kind == clientlayout.MacOS {
		target = filepath.Join(target, filepath.Base(installation.Root))
	}
	if err := copyInstallation(installation.Root, target, installation.Data, installation.BepInEx); err != nil {
		return Instance{}, err
	}
	if installation.Kind == clientlayout.MacOS && filepath.Dir(installation.BepInEx) != filepath.Join(installation.Root, "Contents") {
		if err := copyBepInEx(installation.BepInEx, filepath.Join(staged, "game", "BepInEx")); err != nil {
			return Instance{}, err
		}
		for _, file := range []string{"run_bepinex.sh", "doorstop_config.ini", "libdoorstop.dylib"} {
			path := filepath.Join(filepath.Dir(installation.Root), file)
			if _, err := os.Stat(path); err == nil {
				if err := copyFile(path, filepath.Join(staged, "game", file)); err != nil {
					return Instance{}, err
				}
			}
		}
	}
	for _, dir := range []string{"logs", "logs/crashes", "unity/persistent", "unity/cache", "unity/bundles"} {
		if err := os.MkdirAll(filepath.Join(staged, filepath.FromSlash(dir)), 0o700); err != nil {
			return Instance{}, err
		}
	}
	data, err = json.MarshalIndent(want, "", "  ")
	if err != nil {
		return Instance{}, err
	}
	if err := os.WriteFile(filepath.Join(staged, "instance.json"), append(data, '\n'), 0o600); err != nil {
		return Instance{}, err
	}
	if err := os.Rename(staged, result.Root); err != nil {
		return Instance{}, fmt.Errorf("publish client instance: %w", err)
	}
	result.Created = true
	return result, nil
}

func copyInstallation(source, target, data, bep string) error {
	streaming := filepath.Join(data, "StreamingAssets")
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if path == bep {
			if err := copyBepInEx(path, destination); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("client installation contains a symbolic link: %s", relative)
		}
		if strings.HasSuffix(strings.ToLower(relative), ".log") || strings.HasSuffix(strings.ToLower(relative), ".bak") {
			return nil
		}
		if pathWithin(streaming, path) {
			if err := os.Link(path, destination); err == nil {
				return nil
			}
		}
		return copyFile(path, destination)
	})
}

func copyBepInEx(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		parts := strings.Split(relative, string(filepath.Separator))
		if relative != "." && parts[0] != "core" && parts[0] != "plugins" && parts[0] != "patchers" && parts[0] != "config" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(parts) > 1 && parts[0] == "config" && relative != filepath.Join("config", "BepInEx.cfg") && relative != filepath.Join("config", "bd2.client.json") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(target, relative), 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("BepInEx contains a symbolic link: %s", relative)
		}
		return copyFile(path, filepath.Join(target, relative))
	})
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func copyFile(source, target string) (result error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, input.Close()) }()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported game file: %s", source)
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	return errors.Join(err, output.Close())
}
