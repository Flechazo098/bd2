package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"bd2server/internal/common/util"
)

type Definition struct {
	ID, ProjectPath, Framework, FileName, VersionKey string
	UsesGameSdk                                      bool
	SharedRuntime                                    bool
	Dependencies                                     []string
}

var definitions = []Definition{
	{ID: "game_names", ProjectPath: "GameNames/GameNames.csproj", Framework: "netstandard2.0", FileName: "BD2.GameNames.dll", SharedRuntime: true},
	{ID: "local_identity", ProjectPath: "LocalIdentity/LocalIdentity.csproj", Framework: "netstandard2.1", FileName: "BD2LocalIdentity.dll", VersionKey: "local_identity", UsesGameSdk: true, Dependencies: []string{"game_names"}},
	{ID: "login_ui", ProjectPath: "LoginUI/LoginUI.csproj", Framework: "netstandard2.1", FileName: "BD2LoginUI.dll", VersionKey: "login_ui", UsesGameSdk: true, Dependencies: []string{"game_names"}},
	{ID: "cash_shop", ProjectPath: "CashShop/CashShop.csproj", Framework: "netstandard2.1", FileName: "BD2CashShop.dll", VersionKey: "cash_shop", UsesGameSdk: true, Dependencies: []string{"game_names"}},
	{ID: "fchat", ProjectPath: "FChat/FChat.csproj", Framework: "netstandard2.1", FileName: "BD2FChat.dll", VersionKey: "fchat", UsesGameSdk: true, Dependencies: []string{"game_names", "login_ui"}},
	{ID: "brown_dust_x_accel", ProjectPath: "BrownDustXAccel/BrownDustXAccel.csproj", Framework: "netstandard2.1", FileName: "BD2BrownDustXAccel.dll"},
}

func Definitions() []Definition {
	out := slices.Clone(definitions)
	for i := range out {
		out[i].Dependencies = slices.Clone(out[i].Dependencies)
	}
	return out
}

func validateDefinitions() error {
	byID := make(map[string]Definition, len(definitions))
	outputs, projects := map[string]bool{}, map[string]bool{}
	for _, d := range definitions {
		if err := validateName(d.FileName); err != nil {
			return err
		}
		if d.ID == "" || d.Name() == "" || d.ProjectPath == "" || filepath.IsAbs(d.ProjectPath) || strings.HasPrefix(filepath.Clean(d.ProjectPath), "..") {
			return fmt.Errorf("clientplugin: invalid project definition %q", d.ID)
		}
		_, duplicate := byID[d.ID]
		if duplicate || outputs[strings.ToLower(d.FileName)] || projects[d.Name()] {
			return fmt.Errorf("clientplugin: duplicate plugin definition %s", d.ID)
		}
		byID[d.ID], outputs[strings.ToLower(d.FileName)], projects[d.Name()] = d, true, true
	}
	visited, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("clientplugin: cyclic dependency at %s", id)
		}
		if visited[id] {
			return nil
		}
		d, ok := byID[id]
		if !ok {
			return fmt.Errorf("clientplugin: unknown dependency %s", id)
		}
		visiting[id] = true
		for _, dependency := range d.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id], visited[id] = false, true
		return nil
	}
	for _, d := range definitions {
		if err := visit(d.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d Definition) Name() string {
	return strings.TrimSuffix(filepath.Base(d.ProjectPath), filepath.Ext(d.ProjectPath))
}

func (d Definition) ProjectFile(root string) string {
	return filepath.Join(root, "plugins", filepath.FromSlash(d.ProjectPath))
}

func (d Definition) Output(root string) string {
	return filepath.Join(filepath.Dir(d.ProjectFile(root)), "bin", "Release", d.Framework, d.FileName)
}

func (d Definition) dependencyProjects() map[string]bool {
	projects := map[string]bool{d.Name(): true}
	var visit func(Definition)
	visit = func(current Definition) {
		for _, id := range current.Dependencies {
			for _, dependency := range definitions {
				if dependency.ID == id && !projects[dependency.Name()] {
					projects[dependency.Name()] = true
					visit(dependency)
				}
			}
		}
	}
	visit(d)
	return projects
}

func verifyDependencyOutputs(root string) error {
	for _, d := range definitions {
		projects := d.dependencyProjects()
		for _, dependency := range definitions {
			if !dependency.SharedRuntime || d.ID == dependency.ID || !projects[dependency.Name()] {
				continue
			}
			canonical, err := util.SHA256File(dependency.Output(root))
			if err != nil {
				return err
			}
			copied, err := util.SHA256File(filepath.Join(filepath.Dir(d.Output(root)), dependency.FileName))
			if err != nil {
				return err
			}
			if canonical != copied {
				return fmt.Errorf("clientplugin: %s uses a different %s", d.Name(), dependency.FileName)
			}
		}
	}
	return nil
}

type Artifact struct {
	ID, Name, Source, VersionKey, Version, SHA256 string
}

type Set []Artifact

func Development(root string) Set {
	set := make(Set, 0, len(definitions))
	for _, d := range definitions {
		set = append(set, Artifact{ID: d.ID, Name: d.FileName, Source: d.Output(root), VersionKey: d.VersionKey})
	}
	return set
}

func (set Set) Sources() ([]string, error) {
	if len(set) == 0 {
		return nil, errors.New("clientplugin: plugin set contains no files")
	}
	sources := make([]string, 0, len(set))
	seen := map[string]bool{}
	for _, artifact := range set {
		if err := validateName(artifact.Name); err != nil {
			return nil, err
		}
		if filepath.Base(artifact.Source) != artifact.Name {
			return nil, fmt.Errorf("clientplugin: source name differs from %s", artifact.Name)
		}
		key := strings.ToLower(artifact.Name)
		if seen[key] {
			return nil, fmt.Errorf("clientplugin: duplicate plugin %s", artifact.Name)
		}
		seen[key] = true
		sum, err := util.SHA256File(artifact.Source)
		if err != nil {
			return nil, fmt.Errorf("clientplugin: read %s: %w", artifact.Name, err)
		}
		if artifact.SHA256 != "" && artifact.SHA256 != sum {
			return nil, fmt.Errorf("stored plugin %s failed verification", artifact.Name)
		}
		sources = append(sources, artifact.Source)
	}
	return sources, nil
}

const StoreFile = "plugins.json"

type Store struct {
	Version string       `json:"version"`
	Files   []StoredFile `json:"files"`
}

type StoredFile struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256"`
}

func ReadStore(directory string) (Store, error) {
	var store Store
	raw, err := os.ReadFile(filepath.Join(directory, "plugins", StoreFile))
	if err != nil {
		return store, err
	}
	return store, json.Unmarshal(raw, &store)
}

func Packaged(directory string) (Set, error) {
	store, err := ReadStore(directory)
	if err != nil {
		return nil, fmt.Errorf("clientplugin: read packaged plugins: %w", err)
	}
	set := make(Set, 0, len(store.Files))
	for _, f := range store.Files {
		if err := validateName(f.Name); err != nil {
			return nil, err
		}
		if len(f.SHA256) != 64 {
			return nil, fmt.Errorf("clientplugin: missing SHA-256 for %s", f.Name)
		}
		set = append(set, Artifact{ID: f.ID, Name: f.Name, Source: filepath.Join(directory, "plugins", f.Name), Version: f.Version, SHA256: f.SHA256})
	}
	if _, err := set.Sources(); err != nil {
		return nil, err
	}
	return set, nil
}
