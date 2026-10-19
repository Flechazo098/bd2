package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
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
	"bd2server/internal/common/taskgraph"
)

// BuildDevelopment compiles the client plugins as one dependency graph, sharing
// the game SDK and version preparation across the entry projects.
func BuildDevelopment(ctx context.Context, root, gameDir string, progress io.Writer) error {
	if err := validateDefinitions(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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
	unlock, err := acquireBuildLock(ctx, filepath.Dir(work), progress)
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
	sdkTool := filepath.Join(work, "sdk-tool", "GameSdk.dll")
	var sdkReused bool
	var shared string
	prepareStarted := time.Now()
	graph, err := taskgraph.New(
		taskgraph.Task{Name: "sdkInputs", Run: func(context.Context) error {
			if err := writeBuildInput(sdkInputs, sdkFingerprint); err != nil {
				return err
			}
			if err := writeBuildInput(namesInputs, namesFingerprint); err != nil {
				return err
			}

			sdkReused = currentBuild(sdkManifestPath, sdkKey, digests)
			return nil
		}},
		taskgraph.Task{Name: "sdkCompile", DependsOn: []string{"sdkInputs"}, Run: func(context.Context) error {
			if sdkReused {
				return nil
			}
			_, _ = fmt.Fprintln(progress, "Client plugins: preparing the shared game SDK...")
			if err := runPluginBuild(ctx, root, progress, "build", filepath.Join(root, "plugins", "GameSdk", "GameSdk.csproj"), "-c", "Release", "-o", filepath.Dir(sdkTool), "--nologo", "-v:quiet", "-clp:NoSummary", "-p:BD2SdkCompileInputsFile="+sdkInputs); err != nil {
				return err
			}

			return nil
		}},
		taskgraph.Task{Name: "sdkPrepare", DependsOn: []string{"sdkCompile"}, Run: func(context.Context) error {
			if !sdkReused {
				if err := runPluginBuild(ctx, root, progress, sdkTool, "prepare-embedded", filepath.Join(managed, "Assembly-CSharp.dll"), sdkDir, "--game-version", gameVersion, "--cache-directory", filepath.Join(root, ".build", "game-sdk")); err != nil {
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
			shared = strings.TrimSpace(string(sharedRaw))

			return nil
		}},
		taskgraph.Task{Name: "versionSources", DependsOn: []string{"sdkPrepare"}, Run: func(context.Context) error {
			versionStarted := time.Now()
			_, _ = fmt.Fprintln(progress, "Client plugins: generating version and compilation fingerprints...")
			for _, project := range definitions {
				if project.VersionKey == "" {
					continue
				}
				if err := buildversion.Generate(versionConfig, filepath.Join(versionDir, project.VersionKey+".g.cs"), project.VersionKey); err != nil {
					return err
				}
				inputStamp, err := projectFingerprint(root, inputs, project.Name(), filepath.Join(versionDir, project.VersionKey+".g.cs"))
				if err != nil {
					return err
				}
				if err := writeBuildInput(filepath.Join(compileInputsDir, project.VersionKey+".g.cs"), inputStamp); err != nil {
					return err
				}
			}
			_, _ = fmt.Fprintf(progress, "Client plugins: version sources prepared in %s\n", time.Since(versionStarted).Round(time.Millisecond))

			return nil
		}},
		taskgraph.Task{Name: "pluginsCompile", DependsOn: []string{"versionSources"}, Run: func(context.Context) error {
			compileStarted := time.Now()
			if err := writeAggregateProject(filepath.Join(work, "ClientPlugins.proj"), root); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(progress, "Client plugins: compiling all %d plugins as one parallel dependency graph...\n", len(definitions))
			if err := runPluginBuild(ctx, root, progress, "build", filepath.Join(work, "ClientPlugins.proj"), "-m", "-c", "Release", "--nologo",
				"-p:GameDir="+installation.Root, "-p:BD2ManagedDir="+managed, "-p:BD2BepInExDir="+installation.BepInEx,
				"-p:BD2SdkPrepared=true", "-p:BD2SdkDir="+sdkDir, "-p:BD2SharedSdkDir="+shared,
				"-p:BD2PluginRegistryProps="+filepath.Join(work, "ClientPlugins.props"), "-p:BD2SdkTool="+sdkTool, "-p:GameNamesTable="+filepath.Join(shared, "names.json"),
				"-p:BD2PreparedVersionDir="+versionDir, "-p:BD2CompileInputsDir="+compileInputsDir, "-p:BD2NamesCompileInputsFile="+namesInputs, "-v:quiet", "-clp:NoSummary"); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(progress, "Client plugins: compilation and reobfuscation completed in %s\n", time.Since(compileStarted).Round(time.Millisecond))

			return nil
		}},
		taskgraph.Task{Name: "cacheCommit", DependsOn: []string{"pluginsCompile"}, Run: func(context.Context) error {
			outputs := []string{sdkTool, sdkInputs, strings.TrimSuffix(sdkTool, ".dll") + ".deps.json", strings.TrimSuffix(sdkTool, ".dll") + ".runtimeconfig.json",
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
			outputs = append(outputs, namesInputs)
			for _, project := range definitions {
				outputs = append(outputs, project.Output(root))
				if project.VersionKey != "" {
					outputs = append(outputs, filepath.Join(versionDir, project.VersionKey+".g.cs"), filepath.Join(compileInputsDir, project.VersionKey+".g.cs"))
				}
			}
			if err := verifyDependencyOutputs(root); err != nil {
				return err
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

			return nil
		}},
	)
	if err != nil {
		return err
	}
	if err := graph.Run(ctx, "cacheCommit"); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(progress, "Client plugins: all %d ready in %s\n", len(definitions), time.Since(started).Round(time.Millisecond))
	return nil
}

func acquireBuildLock(ctx context.Context, work string, progress io.Writer) (func(), error) {
	lock := filepath.Join(work, "build.lock")
	file, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	announced := false
	lastNotice := started
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
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
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func runPluginBuild(ctx context.Context, root string, progress io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "dotnet", args...)
	command.Dir = root
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(progress, &output)
	command.Stderr = command.Stdout
	if err := command.Run(); err != nil {
		return fmt.Errorf("build client plugins: %w\n%s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

func writeAggregateProject(path, root string) error {
	var content, properties bytes.Buffer
	content.WriteString("<Project DefaultTargets=\"Build\"><ItemGroup>")
	properties.WriteString("<Project>")
	for _, definition := range definitions {
		content.WriteString("<ClientPlugin Include=\"")
		if err := xml.EscapeText(&content, []byte(definition.ProjectFile(root))); err != nil {
			return err
		}
		content.WriteString("\" />")
		properties.WriteString("<PropertyGroup Condition=\"'$(MSBuildProjectName)' == '")
		if err := xml.EscapeText(&properties, []byte(definition.Name())); err != nil {
			return err
		}
		properties.WriteString("'\"><BD2UsesGameSdk>")
		if definition.UsesGameSdk {
			properties.WriteString("true")
		} else {
			properties.WriteString("false")
		}
		properties.WriteString("</BD2UsesGameSdk><BD2PluginVersionKey>")
		if err := xml.EscapeText(&properties, []byte(definition.VersionKey)); err != nil {
			return err
		}
		properties.WriteString("</BD2PluginVersionKey></PropertyGroup>")
	}
	properties.WriteString("</Project>\n")
	content.WriteString("</ItemGroup><Target Name=\"Restore\"><MSBuild Projects=\"@(ClientPlugin)\" Targets=\"Restore\" BuildInParallel=\"true\" /></Target><Target Name=\"Build\"><MSBuild Projects=\"@(ClientPlugin)\" Targets=\"Build\" BuildInParallel=\"true\" /></Target></Project>\n")
	for filename, data := range map[string][]byte{path: content.Bytes(), filepath.Join(filepath.Dir(path), "ClientPlugins.props"): properties.Bytes()} {
		if previous, err := os.ReadFile(filename); err == nil && bytes.Equal(previous, data) {
			continue
		}
		if err := os.WriteFile(filename, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
