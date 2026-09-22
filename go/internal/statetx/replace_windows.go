//go:build windows

package statetx

import (
	"os"
	"syscall"
	"unsafe"

)

var moveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceFile(source, destination string) error {
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileEx.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0x1|0x8)
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return os.ErrInvalid
	}
	return nil
}

func syncDir(path string) error {
	// Windows does not support FlushFileBuffers on a directory handle. The
	// phase file itself is flushed, and replaceFile uses MoveFileExW with
	// MOVEFILE_WRITE_THROUGH for publishing its directory entry.
	return nil
}
