package main

import (
	"context"
	"errors"
	"os"

	clientplugin "bd2server/internal/client/plugin"
	"bd2server/internal/common/taskgraph"
)

func (t task) plugins(args []string) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	if opts.skipTests || opts.schedulesOnly {
		return errors.New("plugins accepts only --game-dir")
	}
	var game string
	return t.execute("plugins",
		taskgraph.Task{Name: "game:locate", Run: func(context.Context) error {
			var err error
			game, _, _, err = t.gameDirectory(opts.gameDir)
			return err
		}},
		taskgraph.Task{Name: "plugins", DependsOn: []string{"game:locate"}, Run: func(context.Context) error { return clientplugin.BuildDevelopment(t.ctx, t.root, game, os.Stdout) }})
}
