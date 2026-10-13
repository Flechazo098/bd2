package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"bd2server/build/sourcetools"
	"bd2server/internal/common/taskgraph"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bd2w:", err)
		var child *exec.ExitError
		if errors.As(err, &child) && child.ExitCode() > 0 {
			os.Exit(child.ExitCode())
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Usage:
  bd2w build [-GameDir directory] [-SkipTests] [-SchedulesOnly]
  bd2w runClient [client options]
  bd2w runClient2 [--instance player2] [--game-dir directory]
  bd2w plugins [--game-dir directory]
  bd2w runServer [server options]
  bd2w check-csharp [GameSdk GameNames LocalIdentity LoginUI CashShop FChat CaptureEnvironment]
  bd2w sdk pack [--game-dir directory] [--package-version version] [--output-directory directory]
  bd2w sdk verify [--game-dir directory] [--package-version version] [--package-directory directory]
  bd2w sdk update-names --game-mapping file [--game-dir directory] [--version-config file]
  bd2w version-source --config file --output file --plugin name
  bd2w deobfuscate --source directory --mapping file --output directory
  bd2w diff --a directory --b directory --out report.txt [diff options]
  bd2w download-client [--package-dir directory] [--manifest file] [--download | --base]
  bd2w download-resources --output directory --bundle-version version --game-data-version version
  Add --plan to show task dependencies without running them.

Windows: .\bd2w build
Linux/macOS: ./bd2w build
Build automatically selects the native platform and architecture.
The wrappers cache the build tool and refresh it when its sources change.`)
}

func run(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var planOnly bool
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--plan" {
			planOnly = true
		} else {
			filtered = append(filtered, arg)
		}
	}
	args = filtered
	t := task{ctx: ctx, planOnly: planOnly}
	if len(args) == 0 || isHelp(args[0]) {
		usage()
		return nil
	}
	switch args[0] {
	case "deobfuscate":
		return t.execute("deobfuscate", taskgraph.Task{Name: "deobfuscate", Run: func(context.Context) error { return sourcetools.RunDeobfuscate(args[1:], os.Stdout) }})
	case "diff":
		return t.execute("diff", taskgraph.Task{Name: "diff", Run: func(context.Context) error { return sourcetools.RunDiff(args[1:], os.Stdout) }})
	}
	if args[0] != "build" && args[0] != "runClient" && args[0] != "runClient2" && args[0] != "runServer" && args[0] != "sdk" && args[0] != "version-source" && args[0] != "plugins" && args[0] != "check-csharp" && args[0] != "download-client" && args[0] != "download-resources" {
		return fmt.Errorf("unknown task %q; use --help for available commands", args[0])
	}
	if len(args) == 2 && isHelp(args[1]) {
		usage()
		return nil
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	native, err := nativeTarget()
	if err != nil {
		return err
	}
	t.root, t.target = root, native
	switch args[0] {
	case "plugins":
		return t.plugins(args[1:])
	case "runClient2":
		return t.runClient2(args[1:])
	case "runClient":
		if t.planOnly {
			return t.command("go", append([]string{"run", "-tags", "production", "./cmd/bd2client", "--dev", "run", "--plan"}, args[1:]...)...)
		}
		return t.execute("runClient", t.clientIcon(), taskgraph.Task{Name: "runClient", DependsOn: []string{"client:icon"}, Run: func(context.Context) error {
			return t.command("go", append([]string{"run", "-tags", "production", "./cmd/bd2client", "--dev", "run"}, args[1:]...)...)
		}})
	case "runServer":
		if t.planOnly {
			return t.command("go", append([]string{"run", "./cmd/bd2server", "--dev", "run", "--plan"}, args[1:]...)...)
		}
		return t.execute("runServer", taskgraph.Task{Name: "runServer", Run: func(context.Context) error {
			return t.command("go", append([]string{"run", "./cmd/bd2server", "--dev", "run"}, args[1:]...)...)
		}})
	case "version-source":
		return t.execute("version-source", taskgraph.Task{Name: "version-source", Run: func(context.Context) error { return t.generateVersionSource(args[1:]) }})
	case "check-csharp":
		return t.checkCSharp(args[1:])
	case "download-client", "download-resources":
		return t.runDownload(args[0], args[1:])
	case "sdk":
		if len(args) < 2 || isHelp(args[1]) || (len(args) == 3 && isHelp(args[2])) {
			usage()
			return nil
		}
		switch args[1] {
		case "pack":
			return t.sdkPack(args[2:])
		case "verify":
			return t.sdkVerify(args[2:])
		case "update-names":
			return t.sdkUpdateNames(args[2:])
		default:
			return fmt.Errorf("unknown SDK task %q; use sdk pack, verify or update-names", args[1])
		}
	default:
		opts, err := parseOptions(args[1:])
		if err != nil {
			return err
		}
		return t.build(opts)
	}
}

func isHelp(s string) bool {
	return s == "help" || s == "-h" || s == "--help" || strings.EqualFold(s, "-Help")
}
