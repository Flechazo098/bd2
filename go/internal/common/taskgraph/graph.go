// Package taskgraph plans and runs ordered dependency tasks.
package taskgraph

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type Task struct {
	Name      string
	DependsOn []string
	// A nil Run groups dependencies without adding an action.
	Run func(context.Context) error
}

// Graph keeps an immutable copy of task declarations. Execution state belongs
// to each Run; callers own any data captured by their actions.
type Graph struct {
	tasks map[string]Task
}

func New(tasks ...Task) (*Graph, error) {
	graph := &Graph{tasks: make(map[string]Task, len(tasks))}
	names := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.Name) == "" {
			return nil, fmt.Errorf("taskgraph: task name must not be empty")
		}
		if _, exists := graph.tasks[task.Name]; exists {
			return nil, fmt.Errorf("taskgraph: duplicate task %q", task.Name)
		}
		task.DependsOn = slices.Clone(task.DependsOn)
		graph.tasks[task.Name] = task
		names = append(names, task.Name)
	}
	if _, err := graph.Plan(names...); err != nil {
		return nil, err
	}
	return graph, nil
}

// Plan validates all requested targets before returning their dependency order.
// Targets and each task's dependencies retain declaration order, and shared
// dependencies appear only once.
func (g *Graph) Plan(targets ...string) ([]string, error) {
	const visiting, complete = 1, 2
	state := make(map[string]int, len(g.tasks))
	var stack, plan []string
	var visit func(string) error
	visit = func(name string) error {
		task, exists := g.tasks[name]
		if !exists {
			if len(stack) != 0 {
				return fmt.Errorf("taskgraph: task %q depends on unknown task %q", stack[len(stack)-1], name)
			}
			return fmt.Errorf("taskgraph: unknown target %q", name)
		}
		switch state[name] {
		case complete:
			return nil
		case visiting:
			cycle := append(slices.Clone(stack[slices.Index(stack, name):]), name)
			return fmt.Errorf("taskgraph: dependency cycle: %s", strings.Join(cycle, " -> "))
		}
		state[name] = visiting
		stack = append(stack, name)
		for _, dependency := range task.DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = complete
		plan = append(plan, name)
		return nil
	}
	for _, target := range targets {
		if err := visit(target); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// Run completes validation before invoking any action, then runs synchronously.
// Failure or cancellation stops the run before any later task can start.
func (g *Graph) Run(ctx context.Context, targets ...string) error {
	plan, err := g.Plan(targets...)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, name := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		if action := g.tasks[name].Run; action != nil {
			if err := action(ctx); err != nil {
				return fmt.Errorf("taskgraph: task %q: %w", name, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("taskgraph: after task %q: %w", name, err)
		}
	}
	return nil
}
