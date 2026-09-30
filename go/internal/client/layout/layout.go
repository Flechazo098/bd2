// Package layout resolves the supported Windows and macOS Brown Dust II
// installation layouts without relying on the host running bd2client.
package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Kind string

const (
	Windows Kind = "windows"
	MacOS   Kind = "macos"
)

type Installation struct {
	Kind       Kind
	Selected   string
	Root       string
	Executable string
	Data       string
	Resources  string
	Managers   string
	BepInEx    string
	Config     string
	Plugins    string
	Disabled   string
}

func Resolve(selected string) (Installation, error) {
	if strings.TrimSpace(selected) == "" {
		return Installation{}, errors.New("select the Brown Dust II installation directory")
	}
	abs, err := filepath.Abs(strings.TrimSpace(selected))
	if err != nil {
		return Installation{}, fmt.Errorf("resolve game directory: %w", err)
	}
	abs = filepath.Clean(abs)
	if installation, ok := windowsLayout(abs); ok {
		return installation, nil
	}
	if installation, ok := macLayout(abs); ok {
		return installation, nil
	}
	return Installation{}, errors.New("the selected directory is not a complete Brown Dust II Windows or macOS client")
}

func windowsLayout(root string) (Installation, bool) {
	executable := filepath.Join(root, "BrownDust II.exe")
	data := filepath.Join(root, "BrownDust II_Data")
	if !regularFile(executable) || !regularFile(filepath.Join(data, "resources.assets")) {
		return Installation{}, false
	}
	return newInstallation(Windows, root, root, executable, data, filepath.Join(root, "BepInEx")), true
}

func macLayout(selected string) (Installation, bool) {
	candidates := []string{selected}
	if !strings.EqualFold(filepath.Ext(selected), ".app") {
		candidates = append(candidates, filepath.Join(selected, "BrownDust II.app"))
	}
	for _, app := range candidates {
		contents := filepath.Join(app, "Contents")
		executable := filepath.Join(contents, "MacOS", "BrownDust II")
		data := filepath.Join(contents, "Resources", "Data")
		if regularFile(executable) && regularFile(filepath.Join(data, "resources.assets")) {
			// BepInEx Unix distributions are normally extracted beside the
			// .app bundle. Also accept an installation placed inside Contents.
			bepInEx := filepath.Join(filepath.Dir(app), "BepInEx")
			insideBundle := filepath.Join(contents, "BepInEx")
			if directoryExists(insideBundle) && !directoryExists(bepInEx) {
				bepInEx = insideBundle
			}
			return newInstallation(MacOS, selected, app, executable, data, bepInEx), true
		}
	}
	return Installation{}, false
}

func newInstallation(kind Kind, selected, root, executable, data, bepInEx string) Installation {
	return Installation{
		Kind:       kind,
		Selected:   selected,
		Root:       root,
		Executable: executable,
		Data:       data,
		Resources:  filepath.Join(data, "resources.assets"),
		Managers:   filepath.Join(data, "globalgamemanagers"),
		BepInEx:    bepInEx,
		Config:     filepath.Join(bepInEx, "config"),
		Plugins:    filepath.Join(bepInEx, "plugins"),
		Disabled:   filepath.Join(bepInEx, "disabled"),
	}
}

func (i Installation) LaunchTarget() string {
	if i.Kind == MacOS {
		return i.Root
	}
	return i.Executable
}

func (i Installation) SupportedOnHost() bool {
	return (i.Kind == Windows && runtime.GOOS == "windows") || (i.Kind == MacOS && runtime.GOOS == "darwin")
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
