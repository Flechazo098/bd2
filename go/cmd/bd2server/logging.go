package main

import (
	"io"

	"bd2server/internal/server/logging"
)

func configureLogging(writer io.Writer, levelOverride, colorOverride string) error {
	options, err := logging.OptionsFromEnv()
	if err != nil {
		return err
	}
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
