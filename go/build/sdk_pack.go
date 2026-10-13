package main

import (
	"bd2server/internal/common/taskgraph"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type sdkPackage struct {
	task                                                                       task
	options                                                                    sdkOptions
	version, gameVersion, managed, workspace, tool, sdk, shared, table, config string
}

func (t task) sdkPack(args []string) (result error) {
	options, err := t.sdkOptions(args, "pack")
	if err != nil {
		return err
	}
	p := &sdkPackage{task: t, options: options}
	defer func() {
		if p.workspace != "" {
			t.sdkCleanup(p.workspace, &result)
		}
	}()
	return t.execute("sdk:pack", p.tasks()...)
}

func (p *sdkPackage) tasks() []taskgraph.Task {
	t := p.task
	return []taskgraph.Task{
		{Name: "sdk:configuration", Run: func(context.Context) error {
			var err error
			p.version, p.gameVersion, err = t.sdkPackageVersion(p.options)
			if err != nil {
				return err
			}
			_, p.managed, _, err = t.gameDirectory(p.options.gameDir)
			return err
		}},
		{Name: "sdk:workspace", DependsOn: []string{"sdk:configuration"}, Run: func(context.Context) error {
			var err error
			p.workspace, err = t.sdkWorkspace("sdk-pack-")
			if err != nil {
				return err
			}
			p.tool = filepath.Join(p.workspace, "tool")
			p.sdk = filepath.Join(p.workspace, "sdk")
			p.config = filepath.Join(p.workspace, "NuGet.Config")
			return os.MkdirAll(p.options.outputDirectory, 0755)
		}},
		{Name: "sdk:tool", DependsOn: []string{"sdk:workspace"}, Run: func(context.Context) error {
			_, err := t.sdkCommand(t.root, nil, false, "publish", filepath.Join(t.root, "plugins", "GameSdk", "GameSdk.csproj"), "-c", "Release", "--nologo", "-o", p.tool, "-p:Version="+strings.SplitN(p.version, "-", 2)[0], "-p:UseAppHost=false")
			return err
		}},
		{Name: "sdk:prepare", DependsOn: []string{"sdk:tool"}, Run: func(context.Context) error {
			cache := os.Getenv("BD2_GAME_SDK_CACHE")
			if cache == "" {
				cache = filepath.Join(t.root, ".build", "game-sdk")
			}
			if _, err := t.sdkCommand(t.root, map[string]string{"BD2_GAME_SDK_CACHE": cache}, false, filepath.Join(p.tool, "GameSdk.dll"), "prepare-embedded", filepath.Join(p.managed, "Assembly-CSharp.dll"), p.sdk, "--game-version", p.gameVersion); err != nil {
				return err
			}
			var err error
			p.shared, err = sdkReadTrimmed(filepath.Join(p.sdk, "shared-sdk.txt"))
			p.table = filepath.Join(p.shared, "names.json")
			return err
		}},
		{Name: "sdk:names-package", DependsOn: []string{"sdk:prepare"}, Run: func(context.Context) error {
			_, err := t.sdkCommand(t.root, nil, false, "pack", filepath.Join(t.root, "plugins", "GameNames", "GameNames.csproj"), "-c", "Release", "--nologo", "-o", p.options.outputDirectory, "-p:Version="+p.version, "-p:GameNamesTable="+p.table, "-p:BaseIntermediateOutputPath="+filepath.Join(p.workspace, "runtime-obj")+string(filepath.Separator), "-p:OutputPath="+filepath.Join(p.workspace, "runtime-bin")+string(filepath.Separator))
			return err
		}},
		{Name: "sdk:package-restore", DependsOn: []string{"sdk:names-package"}, Run: func(context.Context) error {
			if err := sdkNugetConfig(p.config, p.options.outputDirectory, ""); err != nil {
				return err
			}
			_, err := t.sdkCommand(t.root, nil, false, append([]string{"restore", p.project(), "--configfile", p.config}, p.properties()...)...)
			return err
		}},
		{Name: "sdk:pack", DependsOn: []string{"sdk:package-restore"}, Run: func(context.Context) error {
			args := []string{"pack", p.project(), "-c", "Release", "--no-restore", "--nologo", "-o", p.options.outputDirectory, "-p:BD2ToolPublishDir=" + p.tool}
			if _, err := t.sdkCommand(t.root, nil, false, append(args, p.properties()...)...); err != nil {
				return err
			}
			fmt.Printf("Created BD2.GameNames and BD2.GameSdk %s in %s\n", p.version, p.options.outputDirectory)
			return nil
		}},
	}
}

func (p *sdkPackage) project() string {
	return filepath.Join(p.task.root, "plugins", "GameSdk", "Package", "BD2.GameSdk.Package.csproj")
}
func (p *sdkPackage) properties() []string {
	return []string{"-p:Version=" + p.version, "-p:BD2Packaging=true", "-p:BaseIntermediateOutputPath=" + filepath.Join(p.workspace, "package-obj") + string(filepath.Separator), "-p:OutputPath=" + filepath.Join(p.workspace, "package-bin") + string(filepath.Separator)}
}

func (t task) sdkUpdateNames(args []string) (result error) {
	o, err := t.sdkOptions(args, "update-names")
	if err != nil {
		return err
	}
	if strings.TrimSpace(o.gameMapping) == "" {
		return fmt.Errorf("sdk update-names requires --game-mapping")
	}
	var mapping, managed, workspace, tool, table string
	defer func() {
		if workspace != "" {
			t.sdkCleanup(workspace, &result)
		}
	}()
	return t.execute("sdk:update-names",
		taskgraph.Task{Name: "names:configuration", Run: func(context.Context) error {
			var err error
			mapping, err = filepath.Abs(o.gameMapping)
			if err != nil {
				return err
			}
			if !regular(mapping) {
				return fmt.Errorf("mapping file missing: %s", mapping)
			}
			_, managed, _, err = t.gameDirectory(o.gameDir)
			return err
		}},
		taskgraph.Task{Name: "names:workspace", DependsOn: []string{"names:configuration"}, Run: func(context.Context) error {
			var err error
			workspace, err = t.sdkWorkspace("sdk-names-")
			tool = filepath.Join(workspace, "tool", "GameSdk.dll")
			table = filepath.Join(workspace, "names.json")
			return err
		}},
		taskgraph.Task{Name: "names:tool", DependsOn: []string{"names:workspace"}, Run: func(context.Context) error {
			_, err := t.sdkCommand(t.root, nil, false, "publish", filepath.Join(t.root, "plugins", "GameSdk", "GameSdk.csproj"), "-c", "Release", "--nologo", "-o", filepath.Dir(tool), "-p:UseAppHost=false")
			return err
		}},
		taskgraph.Task{Name: "names:generate", DependsOn: []string{"names:tool"}, Run: func(context.Context) error {
			_, err := t.sdkCommand(t.root, nil, false, tool, "names", filepath.Join(managed, "Assembly-CSharp.dll"), mapping, o.versionConfig, table)
			return err
		}},
		taskgraph.Task{Name: "names:validate", DependsOn: []string{"names:generate"}, Run: func(context.Context) error {
			_, err := t.sdkCommand(t.root, nil, false, tool, "shell", table, filepath.Join(managed, "Assembly-CSharp.dll"), filepath.Join(workspace, "Assembly-CSharp.Readable.dll"))
			return err
		}},
		taskgraph.Task{Name: "sdk:update-names", DependsOn: []string{"names:validate"}, Run: func(context.Context) error {
			destination := filepath.Join(t.root, "plugins", "GameNames", "Mappings", "names.json.gz")
			if err := sdkAtomicCopy(table+".gz", destination); err != nil {
				return err
			}
			fmt.Printf("Updated SDK/runtime shared table: %s. Rebuild SDK and plugins, then pack a new version.\n", destination)
			return nil
		}},
	)
}
