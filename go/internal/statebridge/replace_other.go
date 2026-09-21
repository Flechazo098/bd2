//go:build !windows

package statebridge

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
