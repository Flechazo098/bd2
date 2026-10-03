//go:build windows

package app

import (
	"golang.org/x/sys/windows"
	"syscall"
	"unsafe"
)

func systemGameProxy() string {
	var config struct {
		AutoDetect                   int32
		AutoConfigURL, Proxy, Bypass *uint16
	}
	procedure := syscall.NewLazyDLL("winhttp.dll").NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	globalFree := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalFree")
	result, _, _ := procedure.Call(uintptr(unsafe.Pointer(&config)))
	defer func() {
		for _, pointer := range []*uint16{config.AutoConfigURL, config.Proxy, config.Bypass} {
			if pointer != nil {
				globalFree.Call(uintptr(unsafe.Pointer(pointer)))
			}
		}
	}()
	if result == 0 || config.AutoDetect != 0 || config.AutoConfigURL != nil || config.Proxy == nil {
		return ""
	}
	return sharedWindowsProxy(windows.UTF16PtrToString(config.Proxy))
}
