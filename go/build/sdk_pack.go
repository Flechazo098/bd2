package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (t task) sdkPack(args []string) (result error) {
	o, err := t.sdkOptions(args, "pack")
	if err != nil {
		return err
	}
	version, gameVersion, err := t.sdkPackageVersion(o)
	if err != nil {
		return err
	}
	_, managed, _, err := t.gameDirectory(o.gameDir)
	if err != nil {
		return err
	}
	workspace, err := t.sdkWorkspace("sdk-pack-")
	if err != nil {
		return err
	}
	defer t.sdkCleanup(workspace, &result)
	if err = os.MkdirAll(o.outputDirectory, 0755); err != nil {
		return err
	}
	tool := filepath.Join(workspace, "tool")
	sdk := filepath.Join(workspace, "sdk")
	command := func(args ...string) error { _, err := t.sdkCommand(t.root, nil, false, args...); return err }
	if err = command("publish", filepath.Join(t.root, "plugins", "GameSdk", "GameSdk.csproj"), "-c", "Release", "--nologo", "-o", tool, "-p:Version="+strings.SplitN(version, "-", 2)[0], "-p:UseAppHost=false"); err != nil {
		return err
	}
	toolDLL := filepath.Join(tool, "GameSdk.dll")
	cache := os.Getenv("BD2_GAME_SDK_CACHE")
	if cache == "" {
		cache = filepath.Join(t.root, ".build", "game-sdk")
	}
	if _, err = t.sdkCommand(t.root, map[string]string{"BD2_GAME_SDK_CACHE": cache}, false, toolDLL, "prepare-embedded", filepath.Join(managed, "Assembly-CSharp.dll"), sdk, "--game-version", gameVersion); err != nil {
		return err
	}
	shared, err := sdkReadTrimmed(filepath.Join(sdk, "shared-sdk.txt"))
	if err != nil {
		return err
	}
	table := filepath.Join(shared, "names.json")
	runtimeObj := filepath.Join(workspace, "runtime-obj") + string(filepath.Separator)
	runtimeBin := filepath.Join(workspace, "runtime-bin") + string(filepath.Separator)
	if err = command("pack", filepath.Join(t.root, "plugins", "GameNames", "GameNames.csproj"), "-c", "Release", "--nologo", "-o", o.outputDirectory, "-p:Version="+version, "-p:GameNamesTable="+table, "-p:BaseIntermediateOutputPath="+runtimeObj, "-p:OutputPath="+runtimeBin); err != nil {
		return err
	}
	if err = command(toolDLL, "verify-runtime", table, filepath.Join(runtimeBin, "BD2.GameNames.dll")); err != nil {
		return err
	}
	project := filepath.Join(t.root, "plugins", "GameSdk", "Package", "BD2.GameSdk.Package.csproj")
	config := filepath.Join(workspace, "NuGet.Config")
	if err = sdkNugetConfig(config, o.outputDirectory, ""); err != nil {
		return err
	}
	packageProps := []string{"-p:Version=" + version, "-p:BD2Packaging=true", "-p:BaseIntermediateOutputPath=" + filepath.Join(workspace, "package-obj") + string(filepath.Separator), "-p:OutputPath=" + filepath.Join(workspace, "package-bin") + string(filepath.Separator)}
	if err = command(append([]string{"restore", project, "--configfile", config}, packageProps...)...); err != nil {
		return err
	}
	packArgs := []string{"pack", project, "-c", "Release", "--no-restore", "--nologo", "-o", o.outputDirectory, "-p:BD2ToolPublishDir=" + tool}
	if err = command(append(packArgs, packageProps...)...); err != nil {
		return err
	}
	fmt.Printf("Created BD2.GameNames and BD2.GameSdk %s in %s\n", version, o.outputDirectory)
	return nil
}

func (t task) sdkUpdateNames(args []string) (result error) {
	o, err := t.sdkOptions(args, "update-names")
	if err != nil {
		return err
	}
	if strings.TrimSpace(o.gameMapping) == "" {
		return fmt.Errorf("sdk update-names requires --game-mapping")
	}
	mapping, err := filepath.Abs(o.gameMapping)
	if err != nil {
		return err
	}
	if !regular(mapping) {
		return fmt.Errorf("mapping file missing: %s", mapping)
	}
	_, managed, _, err := t.gameDirectory(o.gameDir)
	if err != nil {
		return err
	}
	workspace, err := t.sdkWorkspace("sdk-names-")
	if err != nil {
		return err
	}
	defer t.sdkCleanup(workspace, &result)
	toolDir := filepath.Join(workspace, "tool")
	command := func(args ...string) error { _, err := t.sdkCommand(t.root, nil, false, args...); return err }
	if err = command("publish", filepath.Join(t.root, "plugins", "GameSdk", "GameSdk.csproj"), "-c", "Release", "--nologo", "-o", toolDir, "-p:UseAppHost=false"); err != nil {
		return err
	}
	tool := filepath.Join(toolDir, "GameSdk.dll")
	assembly := filepath.Join(managed, "Assembly-CSharp.dll")
	table := filepath.Join(workspace, "names.json")
	if err = command(tool, "names", assembly, mapping, o.versionConfig, table); err != nil {
		return err
	}
	if err = command(tool, "shell", table, assembly, filepath.Join(workspace, "Assembly-CSharp.Readable.dll")); err != nil {
		return err
	}
	destination := filepath.Join(t.root, "plugins", "GameNames", "Mappings", "names.json.gz")
	if err = sdkAtomicCopy(table+".gz", destination); err != nil {
		return err
	}
	fmt.Printf("Updated SDK/runtime shared table: %s. Rebuild SDK and plugins, then pack a new version.\n", destination)
	return nil
}
