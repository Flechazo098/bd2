package main

import (
	"context"

	"bd2server/internal/client/iconresource"
	"bd2server/internal/common/taskgraph"
)

func (t task) clientIcon() taskgraph.Task {
	return taskgraph.Task{Name: "client:icon", Run: func(context.Context) error {
		_, err := iconresource.Generate(t.root, t.target.goos, t.target.goarch)
		return err
	}}
}
