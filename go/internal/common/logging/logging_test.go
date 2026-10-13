package logging_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"bd2server/internal/common/logging"
)

func TestHandlerColorsOnlyBuiltinLevel(t *testing.T) {
	var output bytes.Buffer
	handler, err := logging.NewHandler(&output, logging.Options{Level: logging.LevelTrace, Color: logging.ColorAlways})
	if err != nil {
		t.Fatal(err)
	}
	slog.New(handler).Log(context.Background(), logging.LevelTrace, "level=ERROR is message text", "level", "ERROR")
	text := output.String()
	if !strings.Contains(text, "level=\x1b[90mTRACE\x1b[0m msg=") {
		t.Fatalf("TRACE label did not receive its color: %q", text)
	}
	if strings.Count(text, "\x1b[") != 2 || !strings.Contains(text, `msg="level=ERROR is message text" level=ERROR`) {
		t.Fatalf("message or attributes were colored or changed: %q", text)
	}
}

func TestHandlerUsesDynamicLevelAndHonorsColorNever(t *testing.T) {
	var output bytes.Buffer
	var threshold slog.LevelVar
	threshold.Set(slog.LevelWarn)
	handler, err := logging.NewHandler(&output, logging.Options{Level: &threshold, Color: logging.ColorNever})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(handler)
	logger.Info("filtered")
	threshold.Set(slog.LevelDebug)
	logger.Debug("visible")
	text := output.String()
	if strings.Contains(text, "filtered") || !strings.Contains(text, "level=DEBUG msg=visible") || strings.Contains(text, "\x1b[") {
		t.Fatalf("threshold or color policy was ignored: %q", text)
	}
}
