package app

import (
	"io"
	"log/slog"

	"bd2server/internal/common/logging"
)

func ConfigureLogging(writer io.Writer, levelOverride, colorOverride string) error {
	options := logging.Options{Level: slog.LevelInfo, Color: logging.ColorAuto}
	var err error
	if levelOverride != "" {
		options.Level, err = logging.ParseLevel(levelOverride)
		if err != nil {
			return err
		}
	}
	if colorOverride != "" {
		options.Color, err = logging.ParseColorMode(colorOverride)
		if err != nil {
			return err
		}
	}
	_, err = logging.Setup(writer, options)
	return err
}
