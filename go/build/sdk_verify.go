package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"bd2server/internal/common/taskgraph"
)

type sdkVerification struct {
	packages                                                                    *sdkPackage
	workspace, managed, project, output, tool, table, readyPath, navigationPath string
	properties                                                                  []string
	readyTime, navigationTime                                                   time.Time
}

func (t task) sdkVerify(args []string) (result error) {
	options, err := t.sdkOptions(args, "verify")
	if err != nil {
		return err
	}
	options.outputDirectory = options.packageDirectory
	v := &sdkVerification{packages: &sdkPackage{task: t, options: options}}
	defer func() {
		for _, workspace := range []string{v.workspace, v.packages.workspace} {
			if workspace != "" {
				t.sdkCleanup(workspace, &result)
			}
		}
	}()
	nodes := append(v.packages.tasks(),
		taskgraph.Task{Name: "verify:workspace", DependsOn: []string{"sdk:pack"}, Run: func(context.Context) error { return v.prepareWorkspace() }},
		taskgraph.Task{Name: "verify:compile", DependsOn: []string{"verify:workspace"}, Run: func(context.Context) error { return v.build(false) }},
		taskgraph.Task{Name: "verify:outputs", DependsOn: []string{"verify:compile"}, Run: func(context.Context) error { return v.checkOutputs() }},
		taskgraph.Task{Name: "verify:navigation", DependsOn: []string{"verify:outputs"}, Run: func(context.Context) error { return v.checkNavigation() }},
		taskgraph.Task{Name: "verify:incremental", DependsOn: []string{"verify:navigation"}, Run: func(context.Context) error { return v.checkIncremental() }},
		taskgraph.Task{Name: "sdk:verify", DependsOn: []string{"verify:incremental"}, Run: func(context.Context) error { return v.checkVersionRejection() }},
	)
	return t.execute("sdk:verify", nodes...)
}

func (v *sdkVerification) prepareWorkspace() error {
	t, o := v.packages.task, v.packages.options
	game, managed, bep, err := t.gameDirectory(o.gameDir)
	if err != nil {
		return err
	}
	v.managed = managed
	v.workspace, err = t.sdkWorkspace("sdk-verify-")
	if err != nil {
		return err
	}
	consumer := filepath.Join(v.workspace, "consumer")
	for _, name := range []string{"ExamplePlugin.csproj", "Plugin.cs"} {
		if err := copyFile(filepath.Join(t.root, "plugins", "GameSdk", "samples", "ExamplePlugin", name), filepath.Join(consumer, name)); err != nil {
			return err
		}
	}
	v.project = filepath.Join(consumer, "ExamplePlugin.csproj")
	raw, err := os.ReadFile(v.project)
	if err != nil {
		return err
	}
	reference := regexp.MustCompile(`(<PackageReference\s+Include="BD2\.GameSdk"\s+Version=")[^"]+("[^>]*>)`)
	if !reference.Match(raw) {
		return fmt.Errorf("sample has no BD2.GameSdk PackageReference")
	}
	text := reference.ReplaceAllString(string(raw), `${1}`+v.packages.version+`${2}`)
	text = regexp.MustCompile(`<BD2GameVersion>[^<]*</BD2GameVersion>`).ReplaceAllString(text, "<BD2GameVersion>"+v.packages.gameVersion+"</BD2GameVersion>")
	if err := os.WriteFile(v.project, []byte(text), 0644); err != nil {
		return err
	}
	for _, name := range []string{"Directory.Build.props", "Directory.Build.targets"} {
		if err := os.WriteFile(filepath.Join(v.workspace, name), []byte("<Project />\n"), 0644); err != nil {
			return err
		}
	}
	if err := sdkNugetConfig(filepath.Join(v.workspace, "NuGet.Config"), o.packageDirectory, filepath.Join(v.workspace, "packages")); err != nil {
		return err
	}
	v.properties = []string{"-p:GameDir=" + game, "-p:BD2ManagedDir=" + managed, "-p:BD2BepInExDir=" + bep, "-p:BD2GameSdkCache=" + filepath.Join(t.root, ".build", "game-sdk")}
	return nil
}

func (v *sdkVerification) command(args ...string) error {
	_, err := v.packages.task.sdkCommand(v.workspace, nil, false, args...)
	return err
}

func (v *sdkVerification) build(noRestore bool) error {
	args := []string{"build", v.project, "-c", "Release", "--nologo"}
	if noRestore {
		args = append(args, "--no-restore")
	}
	return v.command(append(args, v.properties...)...)
}

func (v *sdkVerification) checkOutputs() error {
	v.output = filepath.Join(filepath.Dir(v.project), "bin", "Release", "netstandard2.1")
	for _, name := range []string{"BD2.GameNames.dll", "ExamplePlugin.dll"} {
		if !regular(filepath.Join(v.output, name)) {
			return fmt.Errorf("missing output: %s", name)
		}
	}
	for _, name := range []string{"Assembly-CSharp.Readable.dll", "Assembly-CSharp.Readable.pdb", "Assembly-CSharp.dll", "GameSdk.dll", "Mono.Cecil.dll", "ICSharpCode.Decompiler.dll", "BepInEx.dll", "0Harmony.dll", "UnityEngine.dll", "ExamplePlugin.pdb", "navigation.json", "sources", "ref", "lib"} {
		if _, err := os.Lstat(filepath.Join(v.output, name)); err == nil {
			return fmt.Errorf("unexpected deployment artifact: %s", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	v.tool = filepath.Join(v.workspace, "packages", "bd2.gamesdk", strings.ToLower(v.packages.version), "tools", "net8.0", "GameSdk.dll")
	sdk := filepath.Join(filepath.Dir(v.project), "obj", "Release", "netstandard2.1", "bd2-game-sdk")
	shared, err := sdkReadTrimmed(filepath.Join(sdk, "shared-sdk.txt"))
	if err != nil {
		return err
	}
	v.table = filepath.Join(shared, "names.json")
	size, err := sdkDirectorySize(sdk)
	if err != nil {
		return err
	}
	if size > 65536 {
		return fmt.Errorf("SDK obj contains %d bytes instead of shared references", size)
	}
	if filepath.Base(filepath.Dir(shared)) != v.packages.gameVersion {
		return fmt.Errorf("shared SDK directory is not grouped by plugin-declared game version")
	}
	v.readyPath = filepath.Join(shared, "ready.txt")
	v.navigationPath = filepath.Join(sdk, "GameSourceNavigation.props")
	for path, stamp := range map[string]*time.Time{v.readyPath: &v.readyTime, v.navigationPath: &v.navigationTime} {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		*stamp = info.ModTime()
	}
	return nil
}

func (v *sdkVerification) checkNavigation() error {
	if err := v.command(v.tool, "verify-navigation", filepath.Dir(v.navigationPath)); err != nil {
		return err
	}
	return v.verifyPlugin()
}

func (v *sdkVerification) verifyPlugin() error {
	return v.command(v.tool, "verify", v.table, filepath.Join(v.output, "ExamplePlugin.dll"), filepath.Join(v.managed, "Assembly-CSharp.dll"))
}

func (v *sdkVerification) checkIncremental() error {
	if err := v.build(true); err != nil {
		return err
	}
	for _, check := range []struct {
		path  string
		stamp time.Time
		error string
	}{
		{v.navigationPath, v.navigationTime, "unchanged navigation props were rewritten"},
		{v.readyPath, v.readyTime, "repeated build unexpectedly regenerated shared SDK"},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			return err
		}
		if !info.ModTime().Equal(check.stamp) {
			return fmt.Errorf("%s", check.error)
		}
	}
	return v.verifyPlugin()
}

func (v *sdkVerification) checkVersionRejection() error {
	for _, invalid := range []string{"", "0.0.0"} {
		args := append([]string{"build", v.project, "-c", "Release", "--no-restore", "--nologo"}, v.properties...)
		args = append(args, "-p:BD2GameVersion="+invalid)
		rejected, err := v.packages.task.sdkCommand(v.workspace, nil, true, args...)
		if err == nil || (invalid == "" && !strings.Contains(rejected, "BD2GameVersion")) || (invalid != "" && !strings.Contains(rejected, "Plugin requires game 0.0.0")) {
			return fmt.Errorf("missing/mismatched game version was not rejected: %q\n%s", invalid, rejected)
		}
	}
	fmt.Println("Verified external PackageReference consumer, source navigation, shared runtime and incremental rebuild")
	return nil
}
