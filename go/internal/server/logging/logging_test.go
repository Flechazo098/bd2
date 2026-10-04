package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestLevelsAndColors(t *testing.T) {
	for _, test := range []struct {
		name, ansi string
		level      slog.Level
	}{
		{"TRACE", "90", LevelTrace}, {"DEBUG", "36", slog.LevelDebug},
		{"INFO", "32", slog.LevelInfo}, {"WARN", "33", slog.LevelWarn}, {"ERROR", "31", slog.LevelError},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			h, err := NewHandler(&out, Options{Level: LevelTrace, Color: ColorAlways})
			if err != nil {
				t.Fatal(err)
			}
			slog.New(h).Log(context.Background(), test.level, "hello", "level", "ERROR", "text", "a\nb")
			want := "level=\x1b[" + test.ansi + "m" + test.name + "\x1b[0m msg=hello level=ERROR text=\"a\\nb\""
			if !strings.Contains(out.String(), want) {
				t.Fatalf("output=%q want fragment=%q", out.String(), want)
			}
		})
	}
}

func TestFilterAndDynamicLevel(t *testing.T) {
	var out bytes.Buffer
	var level slog.LevelVar
	h, _ := NewHandler(&out, Options{Level: &level, Color: ColorNever})
	logger := slog.New(h)
	logger.Log(context.Background(), LevelTrace, "hidden")
	logger.Debug("hidden")
	logger.Info("visible")
	if strings.Contains(out.String(), "hidden") {
		t.Fatal(out.String())
	}
	level.Set(LevelTrace)
	logger.Log(context.Background(), LevelTrace, "trace visible")
	if !strings.Contains(out.String(), "level=TRACE msg=\"trace visible\"") {
		t.Fatal(out.String())
	}
}

func TestGroupsAndConcurrentDerivedLoggers(t *testing.T) {
	var out bytes.Buffer
	h, _ := NewHandler(&out, Options{Color: ColorAlways})
	logger := slog.New(h).With("service", "server").WithGroup("request").With("id", 7)
	var workers sync.WaitGroup
	for i := 0; i < 50; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); logger.Info("handled", slog.Group("result", "ok", true)) }()
	}
	workers.Wait()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 50 {
		t.Fatalf("lines=%d", len(lines))
	}
	for _, line := range lines {
		if !strings.Contains(line, "service=server request.id=7 request.result.ok=true") || strings.Count(line, "\x1b[0m") != 1 {
			t.Fatalf("damaged record %q", line)
		}
	}
}

func TestAutoRedirectedOutputIsPlain(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	h, _ := NewHandler(file, Options{})
	slog.New(h).Warn("redirected")
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\x1b")) {
		t.Fatalf("ANSI in redirected log %q", data)
	}
	var out bytes.Buffer
	h, _ = NewHandler(&out, Options{})
	slog.New(h).Info("buffer")
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal(out.String())
	}
}

func TestEnvironmentAndValidation(t *testing.T) {
	t.Setenv("BD2_LOG_LEVEL", "trace")
	t.Setenv("BD2_LOG_COLOR", "never")
	options, err := OptionsFromEnv()
	if err != nil || options.Level.Level() != LevelTrace || options.Color != ColorNever {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	t.Setenv("BD2_LOG_LEVEL", "invalid")
	if _, err := OptionsFromEnv(); err == nil {
		t.Fatal("invalid level accepted")
	}
	if _, err := ParseColorMode("invalid"); err == nil {
		t.Fatal("invalid color accepted")
	}
	if _, err := NewHandler(nil, Options{}); err == nil {
		t.Fatal("nil writer accepted")
	}
}

func TestAutoColorEnvironment(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if autoColorAllowed() {
		t.Fatal("NO_COLOR presence must suppress auto color")
	}
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	if !autoColorAllowed() {
		t.Fatal("ordinary terminal must allow auto color")
	}
	t.Setenv("TERM", "dumb")
	if autoColorAllowed() {
		t.Fatal("dumb terminal must suppress auto color")
	}
}
