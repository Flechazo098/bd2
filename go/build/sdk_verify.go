package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func (t task) sdkVerify(args []string) (result error) {
	o, err := t.sdkOptions(args, "verify")
	if err != nil {
		return err
	}
	version, gameVersion, err := t.sdkPackageVersion(o)
	if err != nil {
		return err
	}
	game, managed, bep, err := t.gameDirectory(o.gameDir)
	if err != nil {
		return err
	}
	workspace, err := t.sdkWorkspace("sdk-verify-")
	if err != nil {
		return err
	}
	defer t.sdkCleanup(workspace, &result)
	for _, name := range []string{"ExamplePlugin.csproj", "Plugin.cs"} {
		if err = copyFile(filepath.Join(t.root, "plugins", "GameSdk", "samples", "ExamplePlugin", name), filepath.Join(workspace, name)); err != nil {
			return err
		}
	}
	project := filepath.Join(workspace, "ExamplePlugin.csproj")
	raw, err := os.ReadFile(project)
	if err != nil {
		return err
	}
	packageReference := regexp.MustCompile(`(<PackageReference\s+Include="BD2\.GameSdk"\s+Version=")[^"]+("[^>]*>)`)
	if !packageReference.Match(raw) {
		return fmt.Errorf("sample has no BD2.GameSdk PackageReference")
	}
	text := packageReference.ReplaceAllString(string(raw), `${1}`+version+`${2}`)
	text = regexp.MustCompile(`<BD2GameVersion>[^<]*</BD2GameVersion>`).ReplaceAllString(text, "<BD2GameVersion>"+gameVersion+"</BD2GameVersion>")
	if err = os.WriteFile(project, []byte(text), 0644); err != nil {
		return err
	}
	for _, name := range []string{"Directory.Build.props", "Directory.Build.targets"} {
		if err = os.WriteFile(filepath.Join(workspace, name), []byte("<Project />\n"), 0644); err != nil {
			return err
		}
	}
	if err = sdkNugetConfig(filepath.Join(workspace, "NuGet.Config"), o.packageDirectory, filepath.Join(workspace, "packages")); err != nil {
		return err
	}
	sharedCache := filepath.Join(t.root, ".build", "game-sdk")
	properties := []string{"-p:GameDir=" + game, "-p:BD2ManagedDir=" + managed, "-p:BD2BepInExDir=" + bep, "-p:BD2GameSdkCache=" + sharedCache}
	build := func(noRestore bool) error {
		args := []string{"build", project, "-c", "Release", "--nologo"}
		if noRestore {
			args = append(args, "--no-restore")
		}
		_, err := t.sdkCommand(workspace, nil, false, append(args, properties...)...)
		return err
	}
	if err = build(false); err != nil {
		return err
	}
	output := filepath.Join(workspace, "bin", "Release", "netstandard2.1")
	for _, name := range []string{"BD2.GameNames.dll", "ExamplePlugin.dll"} {
		if !regular(filepath.Join(output, name)) {
			return fmt.Errorf("missing output: %s", name)
		}
	}
	for _, name := range []string{"Assembly-CSharp.Readable.dll", "Assembly-CSharp.Readable.pdb", "Assembly-CSharp.dll", "GameSdk.dll", "Mono.Cecil.dll", "ICSharpCode.Decompiler.dll", "BepInEx.dll", "0Harmony.dll", "UnityEngine.dll", "ExamplePlugin.pdb", "navigation.json", "sources", "ref", "lib"} {
		if _, err = os.Lstat(filepath.Join(output, name)); err == nil {
			return fmt.Errorf("unexpected deployment artifact: %s", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	tool := filepath.Join(workspace, "packages", "bd2.gamesdk", strings.ToLower(version), "tools", "net8.0", "GameSdk.dll")
	sdk := filepath.Join(workspace, "obj", "Release", "netstandard2.1", "bd2-game-sdk")
	shared, err := sdkReadTrimmed(filepath.Join(sdk, "shared-sdk.txt"))
	if err != nil {
		return err
	}
	table := filepath.Join(shared, "names.json")
	size, err := sdkDirectorySize(sdk)
	if err != nil {
		return err
	}
	if size > 65536 {
		return fmt.Errorf("SDK obj contains %d bytes instead of shared references", size)
	}
	var example struct {
		Group struct {
			Version string `xml:"BD2GameVersion"`
		} `xml:"PropertyGroup"`
	}
	if err = xml.Unmarshal([]byte(text), &example); err != nil {
		return err
	}
	if filepath.Base(filepath.Dir(shared)) != example.Group.Version {
		return fmt.Errorf("shared SDK directory is not grouped by plugin-declared game version")
	}
	readyPath := filepath.Join(shared, "ready.txt")
	navigationPath := filepath.Join(sdk, "GameSourceNavigation.props")
	stamp := func(path string) (time.Time, error) {
		info, err := os.Stat(path)
		if err != nil {
			return time.Time{}, err
		}
		return info.ModTime(), nil
	}
	readyTime, err := stamp(readyPath)
	if err != nil {
		return err
	}
	navigationTime, err := stamp(navigationPath)
	if err != nil {
		return err
	}
	command := func(args ...string) error { _, err := t.sdkCommand(workspace, nil, false, args...); return err }
	runtime := filepath.Join(output, "BD2.GameNames.dll")
	plugin := filepath.Join(output, "ExamplePlugin.dll")
	assembly := filepath.Join(managed, "Assembly-CSharp.dll")
	if err = command(tool, "verify-runtime", table, runtime); err != nil {
		return err
	}
	if err = command(tool, "verify-navigation", sdk); err != nil {
		return err
	}
	if err = command(tool, "verify", table, plugin, assembly); err != nil {
		return err
	}
	if err = build(true); err != nil {
		return err
	}
	after, err := stamp(navigationPath)
	if err != nil {
		return err
	}
	if !after.Equal(navigationTime) {
		return fmt.Errorf("unchanged navigation props were rewritten")
	}
	after, err = stamp(readyPath)
	if err != nil {
		return err
	}
	if !after.Equal(readyTime) {
		return fmt.Errorf("repeated build unexpectedly regenerated shared SDK")
	}
	if err = command(tool, "verify", table, plugin, assembly); err != nil {
		return err
	}
	for _, invalid := range []string{"", "0.0.0"} {
		args := append([]string{"build", project, "-c", "Release", "--no-restore", "--nologo"}, properties...)
		args = append(args, "-p:BD2GameVersion="+invalid)
		rejected, err := t.sdkCommand(workspace, nil, true, args...)
		if err == nil || (invalid == "" && !strings.Contains(rejected, "BD2GameVersion")) || (invalid != "" && !strings.Contains(rejected, "Plugin requires game 0.0.0")) {
			return fmt.Errorf("missing/mismatched game version was not rejected: %q\n%s", invalid, rejected)
		}
	}
	fmt.Println("Verified external PackageReference consumer, source navigation, shared runtime and incremental rebuild")
	return nil
}
