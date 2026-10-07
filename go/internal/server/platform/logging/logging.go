// Package logging configures the server's structured console logging.
package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

const LevelTrace slog.Level = -8

type ColorMode string

const (
	ColorAuto   ColorMode = "auto"
	ColorAlways ColorMode = "always"
	ColorNever  ColorMode = "never"
)

type Options struct {
	// Level defaults to INFO. A *slog.LevelVar supports changes at runtime.
	Level slog.Leveler
	// Color defaults to auto; always explicitly forces ANSI, including in pipes.
	Color ColorMode
}

// OptionsFromEnv reads process-wide defaults for every server command.
func OptionsFromEnv() (Options, error) {
	options := Options{Level: slog.LevelInfo, Color: ColorAuto}
	if value := os.Getenv("BD2_LOG_LEVEL"); value != "" {
		level, err := ParseLevel(value)
		if err != nil {
			return Options{}, err
		}
		options.Level = level
	}
	if value := os.Getenv("BD2_LOG_COLOR"); value != "" {
		color, err := ParseColorMode(value)
		if err != nil {
			return Options{}, err
		}
		options.Color = color
	}
	return options, nil
}

func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (use trace, debug, info, warn, or error)", value)
	}
}

func ParseColorMode(value string) (ColorMode, error) {
	mode := ColorMode(strings.ToLower(strings.TrimSpace(value)))
	if mode != ColorAuto && mode != ColorAlways && mode != ColorNever {
		return "", fmt.Errorf("invalid log color %q (use auto, always, or never)", value)
	}
	return mode, nil
}

// Setup installs a logger as slog.Default, so existing slog callers use it too.
func Setup(writer io.Writer, options Options) (*slog.Logger, error) {
	handler, err := NewHandler(writer, options)
	if err != nil {
		return nil, err
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger, nil
}

// NewHandler retains slog's quoting, groups, LogValuer resolution and shared
// write lock. Only the built-in level label is colored; attributes stay intact.
func NewHandler(writer io.Writer, options Options) (slog.Handler, error) {
	if writer == nil {
		return nil, fmt.Errorf("log writer is nil")
	}
	mode := options.Color
	if mode == "" {
		mode = ColorAuto
	}
	if _, err := ParseColorMode(string(mode)); err != nil {
		return nil, err
	}
	color := mode == ColorAlways
	if mode == ColorAuto {
		if file, ok := writer.(*os.File); ok && autoColorAllowed() {
			color = terminalSupportsColor(file)
		}
	}
	if color {
		writer = levelColorWriter{writer}
	}
	return slog.NewTextHandler(writer, &slog.HandlerOptions{
		Level: options.Level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 && attr.Key == slog.LevelKey {
				if level, ok := attr.Value.Any().(slog.Level); ok && level == LevelTrace {
					return slog.String(slog.LevelKey, "TRACE")
				}
			}
			return attr
		},
	}), nil
}

func autoColorAllowed() bool {
	_, noColor := os.LookupEnv("NO_COLOR")
	return !noColor && os.Getenv("TERM") != "dumb"
}

type levelColorWriter struct{ io.Writer }

func (w levelColorWriter) Write(data []byte) (int, error) {
	// TextHandler emits time, level, then msg. Search only before msg, so
	// user attributes or messages containing "level=" cannot select a color.
	end := bytes.Index(data, []byte(" msg="))
	if end < 0 {
		return w.Writer.Write(data)
	}
	start := bytes.Index(data[:end], []byte(" level="))
	if start < 0 {
		return w.Writer.Write(data)
	}
	start += len(" level=")
	label := string(data[start:end])
	var color string
	switch {
	case strings.HasPrefix(label, "TRACE"):
		color = "\x1b[90m"
	case strings.HasPrefix(label, "DEBUG"):
		color = "\x1b[36m"
	case strings.HasPrefix(label, "INFO"):
		color = "\x1b[32m"
	case strings.HasPrefix(label, "WARN"):
		color = "\x1b[33m"
	case strings.HasPrefix(label, "ERROR"):
		color = "\x1b[31m"
	default:
		return w.Writer.Write(data)
	}
	output := make([]byte, 0, len(data)+len(color)+4)
	output = append(output, data[:start]...)
	output = append(output, color...)
	output = append(output, data[start:end]...)
	output = append(output, "\x1b[0m"...)
	output = append(output, data[end:]...)
	n, err := w.Writer.Write(output)
	if err == nil && n != len(output) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func Trace(message string, args ...any) { TraceContext(context.Background(), message, args...) }
func TraceContext(ctx context.Context, message string, args ...any) {
	slog.Log(ctx, LevelTrace, message, args...)
}
func Debug(message string, args ...any) { slog.Debug(message, args...) }
func Info(message string, args ...any)  { slog.Info(message, args...) }
func Warn(message string, args ...any)  { slog.Warn(message, args...) }
func Error(message string, args ...any) { slog.Error(message, args...) }
