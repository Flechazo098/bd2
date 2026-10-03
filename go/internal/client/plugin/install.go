package plugin

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	clientlayout "bd2server/internal/client/layout"
)

const (
	BepInExReleasesURL = "https://github.com/BepInEx/BepInEx/releases"
)

type Spec struct {
	fileName string
}

var (
	LocalIdentity = Spec{fileName: "BD2LocalIdentity.dll"}
	LoginUI       = Spec{fileName: "BD2LoginUI.dll"}
	GameNames     = Spec{fileName: "BD2.GameNames.dll"}
)

func (s Spec) FileName() string { return s.fileName }

func (s Spec) validate() error {
	if s.fileName == "" || filepath.Base(s.fileName) != s.fileName || filepath.Ext(s.fileName) != ".dll" {
		return errors.New("clientplugin: invalid plugin specification")
	}
	return nil
}

type Result struct {
	Destination string
	Changed     bool
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

// Install verifies that the user installed BepInEx, then atomically stages the
// packaged plugin and its adjacent shared game-names runtime into its plugins
// directory. It never installs or downloads BepInEx itself.
func Install(spec Spec, gameDir, source string) (Result, error) {
	if err := spec.validate(); err != nil {
		return Result{}, err
	}
	if gameDir == "" || source == "" {
		return Result{}, errors.New("clientplugin: game directory and plugin source are required")
	}
	gameDir = filepath.Clean(gameDir)
	source = filepath.Clean(source)
	installation, err := clientlayout.Resolve(gameDir)
	if err != nil {
		return Result{}, fmt.Errorf("clientplugin: resolve game layout: %w", err)
	}
	bepInEx := filepath.Join(installation.BepInEx, "core", "BepInEx.dll")
	if info, err := os.Stat(bepInEx); err != nil || info.IsDir() {
		return Result{}, fmt.Errorf("clientplugin: BepInEx is not installed; install it manually from %s, then run the client tool again; %s was not copied", BepInExReleasesURL, spec.fileName)
	}
	sourceData, err := os.ReadFile(source)
	if err != nil {
		return Result{}, fmt.Errorf("clientplugin: read packaged %s: %w", spec.fileName, err)
	}
	if len(sourceData) == 0 {
		return Result{}, fmt.Errorf("clientplugin: packaged %s is empty", spec.fileName)
	}
	// Read both artifacts before touching the installation. The runtime table
	// library is shipped beside every plugin, including explicit development paths.
	var runtimeData []byte
	if spec == LocalIdentity || spec == LoginUI {
		runtimeSource := filepath.Join(filepath.Dir(source), GameNames.fileName)
		runtimeData, err = os.ReadFile(runtimeSource)
		if err != nil {
			return Result{}, fmt.Errorf("clientplugin: read shared runtime %s: %w", runtimeSource, err)
		}
		if len(runtimeData) == 0 {
			return Result{}, fmt.Errorf("clientplugin: packaged %s is empty", GameNames.fileName)
		}
	}
	changed := false
	if runtimeData != nil {
		runtimeResult, err := installData(GameNames, installation.Plugins, runtimeData)
		if err != nil {
			return Result{}, err
		}
		changed = runtimeResult.Changed
	}
	result, err := installData(spec, installation.Plugins, sourceData)
	result.Changed = result.Changed || changed
	return result, err
}

func installData(spec Spec, pluginDir string, sourceData []byte) (Result, error) {
	destination := filepath.Join(pluginDir, spec.fileName)
	if installed, err := os.ReadFile(destination); err == nil {
		if bytes.Equal(hash(installed), hash(sourceData)) {
			return Result{Destination: destination}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("clientplugin: inspect installed plugin: %w", err)
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("clientplugin: create plugin directory: %w", err)
	}
	temporary, err := os.CreateTemp(pluginDir, "."+spec.fileName+"-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("clientplugin: create temporary plugin: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = io.Copy(temporary, bytes.NewReader(sourceData)); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Result{}, fmt.Errorf("clientplugin: stage plugin: %w", err)
	}
	if err := replaceFile(temporaryPath, destination); err != nil {
		return Result{}, fmt.Errorf("clientplugin: install plugin (close the game client first): %w", err)
	}
	return Result{Destination: destination, Changed: true}, nil
}

func hash(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
