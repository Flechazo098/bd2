//go:build windows

package plugin

import (
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func fileChangeStamp(path string, info os.FileInfo) (string, bool) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var basic [40]byte
	if windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo, &basic[0], uint32(len(basic))) != nil {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%d:%d", info.Size(), info.ModTime().UnixNano(), binary.LittleEndian.Uint64(basic[:8]), binary.LittleEndian.Uint64(basic[24:32])), true
}
