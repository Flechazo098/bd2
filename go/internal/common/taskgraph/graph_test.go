package taskgraph_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"bd2server/internal/common/taskgraph"
)

func TestSharedProductionFeedsBothConsumersOncePerRun(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "generated.txt")
	var events []string
	productions := 0
	consume := func(name string) func(context.Context) error {
		return func(context.Context) error {
			payload, err := os.ReadFile(artifact)
			if err != nil {
				return err
			}
			events = append(events, name+":"+string(payload))
			return os.WriteFile(filepath.Join(root, name+".txt"), payload, 0o600)
		}
	}
	dependencies := []string{"generate"}
	declarations := []taskgraph.Task{
		{Name: "generate", Run: func(context.Context) error {
			productions++
			events = append(events, fmt.Sprintf("generate:%d", productions))
			return os.WriteFile(artifact, fmt.Appendf(nil, "generation-%d", productions), 0o600)
		}},
		{Name: "server", DependsOn: dependencies, Run: consume("server")},
		{Name: "client", DependsOn: []string{"generate"}, Run: consume("client")},
		{Name: "launch", DependsOn: []string{"client", "server"}, Run: func(context.Context) error {
			client, err := os.ReadFile(filepath.Join(root, "client.txt"))
			if err != nil {
				return err
			}
			server, err := os.ReadFile(filepath.Join(root, "server.txt"))
			if err != nil {
				return err
			}
			if string(client) != string(server) {
				return errors.New("consumers received different generations")
			}
			events = append(events, "launch:"+string(client))
			return os.WriteFile(filepath.Join(root, "launched.txt"), client, 0o600)
		}},
	}
	graph, err := taskgraph.New(declarations...)
	if err != nil {
		t.Fatal(err)
	}
	dependencies[0] = "missing"
	declarations[0].Run = func(context.Context) error { return errors.New("mutated declaration") }
	plan, err := graph.Plan("launch", "server")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan, []string{"generate", "client", "server", "launch"}) {
		t.Fatalf("plan does not preserve declared dependency order: %v", plan)
	}
	plan[0] = "missing"
	for generation := 1; generation <= 2; generation++ {
		if err := graph.Run(context.Background(), "launch", "server"); err != nil {
			t.Fatal(err)
		}
		launched, err := os.ReadFile(filepath.Join(root, "launched.txt"))
		if err != nil || string(launched) != fmt.Sprintf("generation-%d", generation) {
			t.Fatalf("run did not publish current generation: %q, %v", launched, err)
		}
	}
	want := []string{"generate:1", "client:generation-1", "server:generation-1", "launch:generation-1", "generate:2", "client:generation-2", "server:generation-2", "launch:generation-2"}
	if !slices.Equal(events, want) {
		t.Fatalf("shared production repeated or execution order changed: %v", events)
	}
}

func TestInvalidGraphAndTargetListCannotProduceFiles(t *testing.T) {
	for _, invalid := range []struct {
		name, diagnostic string
		tasks            []taskgraph.Task
	}{
		{name: "cycle", diagnostic: "prepare -> verify -> prepare", tasks: []taskgraph.Task{{Name: "prepare", DependsOn: []string{"verify"}}, {Name: "verify", DependsOn: []string{"prepare"}}}},
		{name: "unknown dependency", diagnostic: `depends on unknown task "missing"`, tasks: []taskgraph.Task{{Name: "prepare", DependsOn: []string{"missing"}}}},
		{name: "duplicate", diagnostic: `duplicate task "generate"`, tasks: []taskgraph.Task{{Name: "generate"}}},
		{name: "empty", diagnostic: "must not be empty", tasks: []taskgraph.Task{{Name: "  "}}},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "generated.txt")
			tasks := append([]taskgraph.Task{{Name: "generate", Run: func(context.Context) error { return os.WriteFile(output, []byte("generated"), 0o600) }}}, invalid.tasks...)
			_, err := taskgraph.New(tasks...)
			if err == nil || !strings.Contains(err.Error(), invalid.diagnostic) {
				t.Fatalf("invalid graph was accepted or diagnosis lost: %v", err)
			}
			assertAbsent(t, output)
		})
	}
	output := filepath.Join(t.TempDir(), "generated.txt")
	graph, err := taskgraph.New(taskgraph.Task{Name: "generate", Run: func(context.Context) error { return os.WriteFile(output, []byte("generated"), 0o600) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Run(context.Background(), "generate", "unknown-launch"); err == nil || !strings.Contains(err.Error(), `unknown target "unknown-launch"`) {
		t.Fatalf("late unknown target was not rejected: %v", err)
	}
	assertAbsent(t, output)
}

func TestFailedVerificationDoesNotLaunchAndPreservesCause(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "generated.txt")
	launch := filepath.Join(root, "launched.txt")
	cause := errors.New("bad generated resource")
	graph, err := taskgraph.New(
		taskgraph.Task{Name: "generate", Run: func(context.Context) error { return os.WriteFile(artifact, []byte("invalid"), 0o600) }},
		taskgraph.Task{Name: "verify", DependsOn: []string{"generate"}, Run: func(context.Context) error {
			payload, err := os.ReadFile(artifact)
			if err != nil {
				return err
			}
			if string(payload) != "valid" {
				return cause
			}
			return nil
		}},
		taskgraph.Task{Name: "launch", DependsOn: []string{"verify"}, Run: func(context.Context) error { return os.WriteFile(launch, []byte("started"), 0o600) }},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Run(context.Background(), "launch"); !errors.Is(err, cause) || !strings.Contains(err.Error(), `task "verify"`) {
		t.Fatalf("task failure lost its source or cause: %v", err)
	}
	assertAbsent(t, launch)
}

func TestCancellationStopsLaunchEvenWhenTaskReturnsSuccess(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	graph, err := taskgraph.New(
		taskgraph.Task{Name: "prepare", Run: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(root, "prepared.txt"), []byte("prepared"), 0o600); err != nil {
				return err
			}
			cancel()
			return nil
		}},
		taskgraph.Task{Name: "launch", DependsOn: []string{"prepare"}, Run: func(context.Context) error {
			return os.WriteFile(filepath.Join(root, "launched.txt"), []byte("started"), 0o600)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Run(ctx, "launch"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost after successful action: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "prepared.txt")); err != nil {
		t.Fatal("preparation did not execute", err)
	}
	assertAbsent(t, filepath.Join(root, "launched.txt"))

	preCanceled, stop := context.WithCancel(context.Background())
	stop()
	if err := os.Remove(filepath.Join(root, "prepared.txt")); err != nil {
		t.Fatal(err)
	}
	if err := graph.Run(preCanceled, "launch"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled context was ignored: %v", err)
	}
	assertAbsent(t, filepath.Join(root, "prepared.txt"))
	assertAbsent(t, filepath.Join(root, "launched.txt"))
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected output %s: %v", path, err)
	}
}
