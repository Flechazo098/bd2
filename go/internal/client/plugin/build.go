package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"bd2server/internal/buildversion"
	clientlayout "bd2server/internal/client/layout"
)

var developmentProjects = []struct{ name, key string }{
	{"LocalIdentity", "local_identity"}, {"LoginUI", "login_ui"}, {"CashShop", "cash_shop"}, {"FChat", "fchat"},
}

// BuildDevelopment compiles the client plugins as one dependency graph, sharing
// the game SDK and version preparation across the four entry projects.
func BuildDevelopment(root, gameDir string, progress io.Writer) error {
	started := time.Now()
	installation, err := clientlayout.Resolve(gameDir)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	managed := filepath.Join(installation.Data, "Managed")
	identity := sha256.Sum256([]byte(root + "\x00" + managed + "\x00" + installation.BepInEx))
	work := filepath.Join(root, ".build", "client-plugins", hex.EncodeToString(identity[:12]))
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	unlock, err := acquireBuildLock(filepath.Dir(work), progress)
	if err != nil {
		return err
	}
	defer unlock()
	digestPath := filepath.Join(work, "file-digests.json")
	digests := loadFileDigestCache(digestPath)
	defer func() { _ = digests.save(digestPath) }()
	_, _ = fmt.Fprintln(progress, "Client plugins: checking source, game DLL and output fingerprints...")
	cacheStarted := time.Now()
	inputs, err := snapshotInputs(root, managed, installation.BepInEx, digests)
	if err != nil {
		return err
	}
	fingerprint := digestInputs(inputs)
	manifestPath := filepath.Join(work, "build.json")
	if currentBuild(manifestPath, fingerprint, digests) {
		_, _ = fmt.Fprintf(progress, "Client plugins: inputs and outputs unchanged; SDK, versions, compilation and reobfuscation reused (%s)\n", time.Since(started).Round(time.Millisecond))
		return nil
	}
	_, _ = fmt.Fprintf(progress, "Client plugins: cache check completed in %s; preparing changed inputs...\n", time.Since(cacheStarted).Round(time.Millisecond))
	versionConfig := filepath.Join(root, "versions.json")
	gameVersion, err := buildversion.GameVersion(versionConfig)
	if err != nil {
		return err
	}
	sdkDir := filepath.Join(work, "sdk")
	versionDir := filepath.Join(work, "versions")
	compileInputsDir := filepath.Join(work, "compile-inputs")
	sdkInputs := filepath.Join(compileInputsDir, "sdk.g.cs")
	namesInputs := filepath.Join(compileInputsDir, "names.g.cs")
	sdkFingerprint := dependencyFingerprint(root, inputs, "GameSdk")
	namesFingerprint := dependencyFingerprint(root, inputs, "GameNames")
	sdkInputsKey := map[string]string{"sdk": sdkFingerprint, "game_version": gameVersion}
	for file, sum := range inputs {
		if strings.HasPrefix(file, managed+string(filepath.Separator)) || strings.HasPrefix(file, installation.BepInEx+string(filepath.Separator)) {
			sdkInputsKey[file] = sum
		}
	}
	sdkKey := digestInputs(sdkInputsKey)
	sdkManifestPath := filepath.Join(work, "sdk-build.json")
	if err := writeBuildInput(sdkInputs, sdkFingerprint); err != nil {
		return err
	}
	if err := writeBuildInput(namesInputs, namesFingerprint); err != nil {
		return err
	}
	prepareStarted := time.Now()
	sdkTool := filepath.Join(root, "plugins", "GameSdk", "bin", "Release", "net8.0", "GameSdk.dll")
	if !currentBuild(sdkManifestPath, sdkKey, digests) {
		_, _ = fmt.Fprintln(progress, "Client plugins: preparing the shared game SDK...")
		if err := runPluginBuild(root, progress, "build", filepath.Join(root, "plugins", "GameSdk", "GameSdk.csproj"), "-c", "Release", "--nologo", "-v:quiet", "-clp:NoSummary", "-p:BD2SdkCompileInputsFile="+sdkInputs); err != nil {
			return err
		}
		if err := runPluginBuild(root, progress, sdkTool, "prepare-embedded", filepath.Join(managed, "Assembly-CSharp.dll"), sdkDir, "--game-version", gameVersion); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(progress, "Client plugins: SDK preparation completed in %s\n", time.Since(prepareStarted).Round(time.Millisecond))
	} else {
		_, _ = fmt.Fprintf(progress, "Client plugins: shared SDK reused (%s)\n", time.Since(prepareStarted).Round(time.Millisecond))
	}
	sharedRaw, err := os.ReadFile(filepath.Join(sdkDir, "shared-sdk.txt"))
	if err != nil {
		return err
	}
	shared := strings.TrimSpace(string(sharedRaw))
	versionStarted := time.Now()
	_, _ = fmt.Fprintln(progress, "Client plugins: generating version and compilation fingerprints...")
	for _, project := range developmentProjects {
		if err := buildversion.Generate(versionConfig, filepath.Join(versionDir, project.key+".g.cs"), project.key); err != nil {
			return err
		}
		inputStamp, err := projectFingerprint(root, inputs, project.name, filepath.Join(versionDir, project.key+".g.cs"))
		if err != nil {
			return err
		}
		if err := writeBuildInput(filepath.Join(compileInputsDir, project.key+".g.cs"), inputStamp); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(progress, "Client plugins: version sources prepared in %s\n", time.Since(versionStarted).Round(time.Millisecond))
	compileStarted := time.Now()
	_, _ = fmt.Fprintln(progress, "Client plugins: compiling all four plugins as one parallel dependency graph...")
	if err := runPluginBuild(root, progress, "build", filepath.Join(root, "plugins", "ClientPlugins.proj"), "-m", "-c", "Release", "--nologo",
		"-p:GameDir="+installation.Root, "-p:BD2ManagedDir="+managed, "-p:BD2BepInExDir="+installation.BepInEx,
		"-p:BD2SdkPrepared=true", "-p:BD2SdkDir="+sdkDir, "-p:BD2SharedSdkDir="+shared,
		"-p:BD2PreparedVersionDir="+versionDir, "-p:BD2CompileInputsDir="+compileInputsDir, "-p:BD2NamesCompileInputsFile="+namesInputs, "-v:quiet", "-clp:NoSummary"); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(progress, "Client plugins: compilation and reobfuscation completed in %s\n", time.Since(compileStarted).Round(time.Millisecond))
	outputs := []string{sdkTool, sdkInputs, namesInputs, strings.TrimSuffix(sdkTool, ".dll") + ".deps.json", strings.TrimSuffix(sdkTool, ".dll") + ".runtimeconfig.json",
		filepath.Join(sdkDir, "GameSdkIdentity.g.cs"), filepath.Join(sdkDir, "shared-sdk.txt"), filepath.Join(sdkDir, "GameSourceNavigation.props"),
		filepath.Join(shared, "ref", "Assembly-CSharp.Readable.dll"), filepath.Join(shared, "names.json"), filepath.Join(shared, "names.json.gz")}
	for _, name := range []string{"ready.txt", "navigation.json", "GameSourceNavigation.props", "Assembly-CSharp.Readable.dll", "Assembly-CSharp.Readable.xml",
		"ref/Assembly-CSharp.Readable.xml", "lib/Assembly-CSharp.Readable.dll", "lib/Assembly-CSharp.Readable.pdb", "lib/Assembly-CSharp.Readable.xml", "lib/navigation.json"} {
		outputs = append(outputs, filepath.Join(shared, filepath.FromSlash(name)))
	}
	toolDependencies, err := filepath.Glob(filepath.Join(filepath.Dir(sdkTool), "*.dll"))
	if err != nil {
		return err
	}
	outputs = append(outputs, toolDependencies...)
	sdkOutputs := append([]string(nil), outputs...)
	for _, project := range developmentProjects {
		output := filepath.Join(root, "plugins", project.name, "bin", "Release", "netstandard2.1")
		outputs = append(outputs, filepath.Join(output, "BD2"+project.name+".dll"), filepath.Join(output, GameNames.fileName), filepath.Join(versionDir, project.key+".g.cs"), filepath.Join(compileInputsDir, project.key+".g.cs"))
	}
	sums := make(map[string]string, len(outputs))
	for _, output := range outputs {
		sum, err := digests.digest(output)
		if err != nil {
			return err
		}
		sums[output] = sum
	}
	finalInputs, err := snapshotInputs(root, managed, installation.BepInEx, digests)
	if err != nil {
		return err
	}
	if digestInputs(finalInputs) != fingerprint {
		return errors.New("client plugin inputs changed during compilation; run preparation again")
	}
	if err := saveBuildManifest(manifestPath, buildManifest{Input: fingerprint, Outputs: sums}); err != nil {
		return err
	}
	sdkSums := make(map[string]string, len(sdkOutputs))
	for _, output := range sdkOutputs {
		sdkSums[output] = sums[output]
	}
	if err := saveBuildManifest(sdkManifestPath, buildManifest{Input: sdkKey, Outputs: sdkSums}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(progress, "Client plugins: all four ready in %s\n", time.Since(started).Round(time.Millisecond))
	return nil
}

func acquireBuildLock(work string, progress io.Writer) (func(), error) {
	lock := filepath.Join(work, "build.lock")
	file, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	announced := false
	lastNotice := started
	for {
		locked, err := tryBuildFileLock(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return func() { _ = file.Close() }, nil
		}
		if time.Since(started) > 30*time.Minute {
			_ = file.Close()
			return nil, fmt.Errorf("client plugin build lock still held at %s", lock)
		}
		if !announced {
			_, _ = fmt.Fprintln(progress, "Client plugins: waiting for the current build to finish...")
			announced = true
			lastNotice = time.Now()
		} else if time.Since(lastNotice) >= time.Minute {
			_, _ = fmt.Fprintf(progress, "Client plugins: still waiting for the current build (%s)...\n", time.Since(started).Round(time.Second))
			lastNotice = time.Now()
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func runPluginBuild(root string, progress io.Writer, args ...string) error {
	command := exec.Command("dotnet", args...)
	command.Dir = root
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "BD2_GAME_SDK_CACHE") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "BD2_GAME_SDK_CACHE="+filepath.Join(root, ".build", "game-sdk"))
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(progress, &output)
	command.Stderr = command.Stdout
	if err := command.Run(); err != nil {
		return fmt.Errorf("build client plugins: %w\n%s", err, strings.TrimSpace(output.String()))
	}
	return nil
}
