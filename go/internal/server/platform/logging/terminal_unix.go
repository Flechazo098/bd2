//go:build !windows

package logging

import (
	"github.com/mattn/go-isatty"
	"os"
)

func terminalSupportsColor(file *os.File) bool { return isatty.IsTerminal(file.Fd()) }
