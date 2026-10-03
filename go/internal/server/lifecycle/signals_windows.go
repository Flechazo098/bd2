//go:build windows

package lifecycle

import "os"

func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt} }
