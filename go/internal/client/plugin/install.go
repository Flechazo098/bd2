package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	clientlayout "bd2server/internal/client/layout"
	commonconfig "bd2server/internal/common/config"
)

const (
	BepInExReleasesURL = "https://github.com/BepInEx/BepInEx/releases"
	// managedFile records which files in BepInEx/plugins this launcher owns,
	// so a later set can remove a dropped plugin without touching others.
	managedFile = ".bd2-managed.json"
)

type Spec struct {
	fileName string
}

var (
	LocalIdentity = Spec{fileName: "BD2LocalIdentity.dll"}
	LoginUI       = Spec{fileName: "BD2LoginUI.dll"}
	CashShop      = Spec{fileName: "BD2CashShop.dll"}
	FChat         = Spec{fileName: "BD2FChat.dll"}
	GameNames     = Spec{fileName: "BD2.GameNames.dll"}
)

// Shipped lists every plugin the launcher installs, including the shared
// runtime the others depend on.
var Shipped = []Spec{LocalIdentity, LoginUI, CashShop, FChat, GameNames}

func (s Spec) FileName() string { return s.fileName }

func (s Spec) validate() error {
	if s.fileName == "" || filepath.Base(s.fileName) != s.fileName || path.Base(s.fileName) != s.fileName || strings.ContainsAny(s.fileName, `\:`) || filepath.Ext(s.fileName) != ".dll" {
		return errors.New("clientplugin: invalid plugin specification")
	}
	return nil
}

type Result struct {
	Destination string `json:"destination"`
	Changed     bool   `json:"changed"`
}

type SetResult struct {
	Changed bool     `json:"changed"`
	Files   []Result `json:"files"`
	Removed []string `json:"removed,omitempty"`
}

func ResolvePackaged(spec Spec, explicit string) (string, error) {
	if err := spec.validate(); err != nil {
		return "", err
	}
	if explicit != "" {
		return filepath.Clean(explicit), nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("clientplugin: resolve client tool executable: %w", err)
	}
	return filepath.Join(filepath.Dir(executable), "plugins", spec.fileName), nil
}

var installMu sync.Mutex

type staged struct {
	name, temporary, destination, backup string
	data                                 []byte
}

// InstallSet installs sources into the game's BepInEx/plugins as one unit:
// every file is staged first, then all are swapped in, and a failure part way
// restores the files that were already replaced. Only files listed in the
// previous managed set are ever removed; other plugins, configuration and
// language files are left alone. It never installs BepInEx itself.
func InstallSet(gameDir string, sources []string) (SetResult, error) {
	installMu.Lock()
	defer installMu.Unlock()
	if gameDir == "" || len(sources) == 0 {
		return SetResult{}, errors.New("clientplugin: game directory and plugin sources are required")
	}
	installation, err := clientlayout.Resolve(filepath.Clean(gameDir))
	if err != nil {
		return SetResult{}, fmt.Errorf("clientplugin: resolve game layout: %w", err)
	}
	if info, err := os.Stat(filepath.Join(installation.BepInEx, "core", "BepInEx.dll")); err != nil || info.IsDir() {
		return SetResult{}, fmt.Errorf("clientplugin: BepInEx is not installed; install it manually from %s, then run the client tool again", BepInExReleasesURL)
	}
	directory := installation.Plugins
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return SetResult{}, fmt.Errorf("clientplugin: create plugin directory: %w", err)
	}
	var files []*staged
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		name := filepath.Base(source)
		if err := (Spec{fileName: name}).validate(); err != nil {
			return SetResult{}, err
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return SetResult{}, fmt.Errorf("clientplugin: read packaged %s: %w", name, err)
		}
		if len(data) == 0 {
			return SetResult{}, fmt.Errorf("clientplugin: packaged %s is empty", name)
		}
		for _, existing := range names {
			if strings.EqualFold(existing, name) {
				return SetResult{}, fmt.Errorf("clientplugin: duplicate plugin %s", name)
			}
		}
		names = append(names, name)
		files = append(files, &staged{name: name, destination: filepath.Join(directory, name), data: data})
	}
	result := SetResult{}
	var changed []*staged
	for _, f := range files {
		installed, err := os.ReadFile(f.destination)
		if err == nil && bytes.Equal(installed, f.data) {
			result.Files = append(result.Files, Result{Destination: f.destination})
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return SetResult{}, fmt.Errorf("clientplugin: inspect installed plugin: %w", err)
		}
		changed = append(changed, f)
		result.Files = append(result.Files, Result{Destination: f.destination, Changed: true})
	}
	previous, err := readManaged(directory)
	if err != nil {
		return SetResult{}, err
	}
	var dropped []string
	for _, name := range previous {
		if !slices.ContainsFunc(names, func(candidate string) bool { return strings.EqualFold(candidate, name) }) {
			dropped = append(dropped, name)
		}
	}
	defer func() {
		for _, f := range changed {
			if f.temporary != "" {
				_ = os.Remove(f.temporary)
			}
		}
	}()
	for _, f := range changed {
		temporary, err := stageFile(directory, f.name, f.data)
		if err != nil {
			return SetResult{}, err
		}
		f.temporary = temporary
	}
	backupDir, err := os.MkdirTemp(directory, ".bd2-transaction-*")
	if err != nil {
		return SetResult{}, err
	}
	var replaced []*staged
	restore := func(cause error) (SetResult, error) {
		var failures []error
		for _, f := range slices.Backward(replaced) {
			if f.backup != "" {
				if err := commonconfig.ReplaceFile(f.backup, f.destination); err != nil {
					failures = append(failures, fmt.Errorf("restore %s: %w", f.name, err))
				}
			} else if err := os.Remove(f.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, err)
			}
		}
		if len(failures) != 0 {
			return SetResult{}, fmt.Errorf("clientplugin: install failed; rollback incomplete (backups in %s): %w", backupDir, errors.Join(append([]error{cause}, failures...)...))
		}
		_ = os.RemoveAll(backupDir)
		return SetResult{}, fmt.Errorf("clientplugin: install failed; previous plugins restored: %w", cause)
	}
	swap := func(f *staged) error {
		info, err := os.Lstat(f.destination)
		if err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("clientplugin: %s is not a regular file", f.destination)
			}
			f.backup = filepath.Join(backupDir, f.name)
			if err := os.Rename(f.destination, f.backup); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		replaced = append(replaced, f)
		if f.temporary != "" {
			if err := commonconfig.ReplaceFile(f.temporary, f.destination); err != nil {
				return err
			}
			f.temporary = ""
		}
		return nil
	}
	for _, f := range changed {
		if err := swap(f); err != nil {
			return restore(err)
		}
	}
	for _, name := range dropped {
		f := &staged{name: name, destination: filepath.Join(directory, name)}
		if err := swap(f); err != nil {
			return restore(err)
		}
		if f.backup != "" {
			result.Removed = append(result.Removed, name)
		}
	}
	if !slices.Equal(previous, names) {
		if err := commonconfig.Save(filepath.Join(directory, managedFile), names, commonconfig.WriteOptions{}); err != nil {
			return restore(fmt.Errorf("record managed plugins: %w", err))
		}
	}
	if err := os.RemoveAll(backupDir); err != nil {
		return result, fmt.Errorf("clientplugin: plugins installed but backup cleanup failed: %w", err)
	}

	result.Changed = len(changed) != 0 || len(result.Removed) != 0 || !slices.Equal(previous, names)
	return result, nil
}

func stageFile(directory, name string, data []byte) (string, error) {
	temporary, err := os.CreateTemp(directory, "."+name+"-*.tmp")
	if err != nil {
		return "", fmt.Errorf("clientplugin: create temporary plugin: %w", err)
	}
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary.Name())
		return "", fmt.Errorf("clientplugin: stage plugin: %w", err)
	}
	return temporary.Name(), nil
}

func readManaged(directory string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(directory, managedFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("clientplugin: read managed plugins: %w", err)
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, fmt.Errorf("clientplugin: decode managed plugins: %w", err)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("clientplugin: duplicate managed plugin %q", name)
		}
		seen[strings.ToLower(name)] = true
		if err := (Spec{fileName: name}).validate(); err != nil {
			return nil, fmt.Errorf("clientplugin: invalid managed plugin %q", name)
		}
	}
	return names, nil
}

// Installed reports whether the game's plugin directory already holds
// exactly these files.
func Installed(gameDir string, sources []string) (bool, error) {
	installMu.Lock()
	defer installMu.Unlock()
	installation, err := clientlayout.Resolve(filepath.Clean(gameDir))
	if err != nil {
		return false, err
	}
	for _, source := range sources {
		want, err := os.ReadFile(source)
		if err != nil {
			return false, err
		}
		have, err := os.ReadFile(filepath.Join(installation.Plugins, filepath.Base(source)))
		if err != nil || !bytes.Equal(have, want) {
			return false, nil
		}
	}
	previous, err := readManaged(installation.Plugins)
	if err != nil {
		return false, err
	}
	for _, name := range previous {
		if !slices.ContainsFunc(sources, func(source string) bool { return strings.EqualFold(filepath.Base(source), name) }) {
			return false, nil
		}
	}
	return true, nil
}
