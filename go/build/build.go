package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
  bd2w runServer [server options]
  bd2w check-csharp [GameSdk GameNames LocalIdentity LoginUI CashShop CaptureEnvironment]
  bd2w sdk pack [--game-dir directory] [--package-version version] [--output-directory directory]
  bd2w sdk verify [--game-dir directory] [--package-version version] [--package-directory directory]
  bd2w sdk update-names --game-mapping file [--game-dir directory] [--version-config file]
  bd2w version-source --config file --output file --plugin name

Windows: .\bd2w build
Linux/macOS: ./bd2w build
Build automatically selects the native platform and architecture.
The wrappers cache the build tool and refresh it when its sources change.`)
}

func run(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		usage()
		return nil
	}
	if args[0] != "build" && args[0] != "runClient" && args[0] != "runServer" && args[0] != "sdk" && args[0] != "version-source" {
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
	t := task{root, native}
	switch args[0] {
	case "runClient":
		return t.command("go", append([]string{"run", "-tags", "production", "./cmd/bd2client", "--dev", "run"}, args[1:]...)...)
	case "runServer":
		return t.command("go", append([]string{"run", "./cmd/bd2server", "--dev", "run"}, args[1:]...)...)
	case "version-source":
		return t.generateVersionSource(args[1:])
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
