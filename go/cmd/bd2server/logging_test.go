package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"bd2server/internal/server/logging"
)

func TestServeLoggingOverridesEnvironmentAndEnablesTrace(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	t.Setenv("BD2_LOG_LEVEL", "warn")
	t.Setenv("BD2_LOG_COLOR", "never")
	var output bytes.Buffer
	if err := configureLogging(&output, "trace", "always"); err != nil {
		t.Fatal(err)
	}
	logging.Trace("trace enabled")
	slog.Info("existing callers use new handler")
	if !strings.Contains(output.String(), "\x1b[90mTRACE\x1b[0m") || !strings.Contains(output.String(), "\x1b[32mINFO\x1b[0m") {
		t.Fatalf("log output=%q", output.String())
	}
	output.Reset()
	if err := configureLogging(&output, "", ""); err != nil {
		t.Fatal(err)
	}
	slog.Info("filtered")
	slog.Warn("plain warning")
	if strings.Contains(output.String(), "filtered") || strings.Contains(output.String(), "\x1b[") || !strings.Contains(output.String(), "level=WARN") {
		t.Fatalf("env log output=%q", output.String())
	}
}
