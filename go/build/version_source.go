package main

import (
	"errors"
	"flag"
	"io"

	"bd2server/internal/buildversion"
)

func (t task) generateVersionSource(args []string) error {
	options := flag.NewFlagSet("version-source", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	config := options.String("config", "", "version configuration")
	output := options.String("output", "", "generated C# source")
	plugin := options.String("plugin", "", "plugin version key")
	if err := options.Parse(args); err != nil {
		return err
	}
	if options.NArg() != 0 || *config == "" || *output == "" || *plugin == "" {
		return errors.New("version-source requires --config, --output and --plugin, with no positional arguments")
	}
	return buildversion.Generate(*config, *output, *plugin)
}
