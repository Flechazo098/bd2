//go:build !windows

package lifecycle

import (
	"os"
	"syscall"
)

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGUSR2}
}
