//go:build windows

package statebridge

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32MoveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

func replaceFile(source, destination string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := kernel32MoveFileEx.Call(
		uintptr(unsafe.Pointer(sourcePtr)),
		uintptr(unsafe.Pointer(destinationPtr)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return os.ErrInvalid
	}
	return nil
}
