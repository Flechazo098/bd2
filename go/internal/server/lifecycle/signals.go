package lifecycle

import "os"

// ShutdownSignals is kept platform-specific because Windows does not define
// the Unix rolling-restart signal.
func ShutdownSignals() []os.Signal { return shutdownSignals() }
