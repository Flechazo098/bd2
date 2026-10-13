//go:build !windows

package plugin

import "os"

func fileChangeStamp(_ string, _ os.FileInfo) (string, bool) {
	return "", false
}
