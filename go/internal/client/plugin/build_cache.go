package plugin

import (
	"bd2server/internal/common/util"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type buildManifest struct {
	Input   string            `json:"input"`
	Outputs map[string]string `json:"outputs"`
}

type cachedFileDigest struct {
	Stamp string `json:"stamp"`
	Hash  string `json:"hash"`
}

type fileDigestCache map[string]cachedFileDigest

func loadFileDigestCache(path string) fileDigestCache {
	cache := make(fileDigestCache)
	data, err := os.ReadFile(path)
	if err == nil && (json.Unmarshal(data, &cache) != nil || cache == nil) {
		return make(fileDigestCache)
	}
	return cache
}

func (cache fileDigestCache) digest(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() < 1024*1024 {
		return util.SHA256File(path)
	}
	stamp, reliable := fileChangeStamp(path, info)
	if reliable {
		if previous, ok := cache[path]; ok && previous.Stamp == stamp {
			return previous.Hash, nil
		}
	}
	sum, err := util.SHA256File(path)
	if err == nil && reliable {
		cache[path] = cachedFileDigest{Stamp: stamp, Hash: sum}
	}
	return sum, err
}

func (cache fileDigestCache) save(path string) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return nil
	}
	return saveBuildJSON(path, data)
}

func snapshotInputs(root, managed, bep string, cache fileDigestCache) (map[string]string, error) {
	files, err := developmentInputs(root, managed, bep)
	if err != nil {
		return nil, err
	}
	inputs := make(map[string]string, len(files))
	for _, file := range files {
		sum, err := cache.digest(file)
		if err != nil {
			return nil, err
		}
		inputs[file] = sum
	}
	return inputs, nil
}

func projectFingerprint(root string, inputs map[string]string, project, versionSource string) (string, error) {
	var definition Definition
	for _, candidate := range definitions {
		if candidate.Name() == project {
			definition = candidate
			break
		}
	}
	relevant := selectDependencyInputs(root, inputs, definition)
	if versionSource != "" {
		sum, err := util.SHA256File(versionSource)
		if err != nil {
			return "", err
		}
		relevant[versionSource] = sum
	}
	return digestInputs(relevant), nil
}

func selectDependencyInputs(root string, inputs map[string]string, definition Definition) map[string]string {
	projects := definition.dependencyProjects()
	relevant := make(map[string]string)
	for file, sum := range inputs {
		if strings.HasSuffix(file, ".go") || file == filepath.Join(root, "versions.json") || file == filepath.Join(root, "go", "go.mod") || file == filepath.Join(root, "go", "go.sum") {
			continue
		}
		relative, err := filepath.Rel(filepath.Join(root, "plugins"), file)
		if err != nil {
			continue
		}
		folder, _, nested := strings.Cut(filepath.ToSlash(relative), "/")
		if nested && folder != ".." {
			registered := false
			dependency := false
			for _, candidate := range definitions {
				if strings.HasPrefix(filepath.ToSlash(relative), filepath.ToSlash(filepath.Dir(candidate.ProjectPath))+"/") {
					registered = true
					dependency = projects[candidate.Name()]
					break
				}
			}
			if registered && !dependency {
				continue
			}
			if folder == "GameSdk" && folder != definition.Name() && !definition.UsesGameSdk {
				continue
			}
			if !registered && folder != "GameSdk" && folder != "Shared" {
				continue
			}
		}
		relevant[file] = sum
	}
	return relevant
}

func dependencyFingerprint(root string, inputs map[string]string, project string) string {
	definition := Definition{ProjectPath: project + "/" + project + ".csproj"}
	for _, candidate := range definitions {
		if candidate.Name() == project {
			definition = candidate
			break
		}
	}
	if project == "GameSdk" {
		for _, candidate := range definitions {
			if candidate.SharedRuntime {
				definition.Dependencies = append(definition.Dependencies, candidate.ID)
			}
		}
		definition.UsesGameSdk = true
	}
	return digestInputs(selectDependencyInputs(root, inputs, definition))
}

func developmentInputs(root, managed, bep string) ([]string, error) {
	var files []string
	for _, directory := range []string{filepath.Join(root, "plugins"), filepath.Join(root, "go", "internal", "client", "plugin"), filepath.Join(root, "go", "internal", "buildversion")} {
		err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "bin" || entry.Name() == "obj" || entry.Name() == ".git" || entry.Name() == "CaptureEnvironment" || entry.Name() == "samples" {
					return filepath.SkipDir
				}
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".cs", ".go", ".csproj", ".props", ".targets", ".proj", ".json", ".gz", ".xml", ".resx", ".resources", ".editorconfig":
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, directory := range []string{managed, filepath.Join(bep, "core")} {
		err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".dll") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"versions.json", ".editorconfig", "global.json", "NuGet.Config", "Directory.Build.props", "Directory.Build.targets", "Directory.Packages.props", "go/go.mod", "go/go.sum"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return files, nil
}

func digestInputs(inputs map[string]string) string {
	files := make([]string, 0, len(inputs))
	for file := range inputs {
		files = append(files, file)
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, file := range files {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\n", file, inputs[file])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeBuildInput(path, fingerprint string) error {
	content := []byte("// Client plugin compilation inputs: " + fingerprint + "\n")
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, content) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func currentBuild(path, fingerprint string, cache fileDigestCache) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var manifest buildManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Input != fingerprint || len(manifest.Outputs) == 0 {
		return false
	}
	for output, expected := range manifest.Outputs {
		sum, err := cache.digest(output)
		if err != nil || sum != expected {
			return false
		}
	}
	return true
}

func saveBuildManifest(path string, manifest buildManifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return saveBuildJSON(path, raw)
}

func saveBuildJSON(path string, raw []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".build-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return util.ReplaceFile(temporary.Name(), path)
}
