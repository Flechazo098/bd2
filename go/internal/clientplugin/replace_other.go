//go:build !windows

package clientplugin

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
