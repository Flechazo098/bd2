//go:build windows

package app

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1

	fosNoChangeDir     = 0x00000008
	fosPickFolders     = 0x00000020
	fosForceFileSystem = 0x00000040
	fosPathMustExist   = 0x00000800
	fosDontAddToRecent = 0x02000000

	sigdnFileSystemPath = 0x80058000
	errorCancelled      = 0x800704c7
)

var (
	ole32DLL            = windows.NewLazySystemDLL("ole32.dll")
	user32DLL           = windows.NewLazySystemDLL("user32.dll")
	coInitializeEx      = ole32DLL.NewProc("CoInitializeEx")
	coUninitialize      = ole32DLL.NewProc("CoUninitialize")
	coCreateInstance    = ole32DLL.NewProc("CoCreateInstance")
	coTaskMemFree       = ole32DLL.NewProc("CoTaskMemFree")
	getForegroundWindow = user32DLL.NewProc("GetForegroundWindow")
	clsidFileOpenDialog = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
)

// comObject is sufficient for IFileOpenDialog and IShellItem because COM
// interfaces begin with a pointer to a vtable. The methods used below are
// selected by their documented vtable positions.
type comObject struct {
	vtable *[29]uintptr
}

func browseForDirectory(titleText string) (string, error) {
	// COM apartment state belongs to an OS thread. Keep this handler on one
	// thread from initialization until every interface has been released.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	result, _, _ := coInitializeEx.Call(0, coinitApartmentThreaded)
	if hresultFailed(result) {
		return "", hresultError("initialize Windows directory picker", result)
	}
	defer coUninitialize.Call()

	var dialog *comObject
	result, _, _ = coCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if hresultFailed(result) {
		return "", hresultError("create Windows directory picker", result)
	}
	if dialog == nil {
		return "", fmt.Errorf("create Windows directory picker: the system returned no dialog")
	}
	defer comRelease(dialog)

	var options uint32
	result = comCall(dialog, 10, uintptr(unsafe.Pointer(&options))) // IFileDialog::GetOptions
	if hresultFailed(result) {
		return "", hresultError("read Windows directory picker options", result)
	}
	options |= fosNoChangeDir | fosPickFolders | fosForceFileSystem | fosPathMustExist | fosDontAddToRecent
	result = comCall(dialog, 9, uintptr(options)) // IFileDialog::SetOptions
	if hresultFailed(result) {
		return "", hresultError("set Windows directory picker options", result)
	}

	title, err := windows.UTF16PtrFromString(titleText)
	if err != nil {
		return "", fmt.Errorf("set Windows directory picker title: %w", err)
	}
	result = comCall(dialog, 17, uintptr(unsafe.Pointer(title))) // IFileDialog::SetTitle
	runtime.KeepAlive(title)
	if hresultFailed(result) {
		return "", hresultError("set Windows directory picker title", result)
	}

	owner, _, _ := getForegroundWindow.Call()
	result = comCall(dialog, 3, owner) // IModalWindow::Show
	if uint32(result) == errorCancelled {
		return "", nil
	}
	if hresultFailed(result) {
		return "", hresultError("show Windows directory picker", result)
	}

	var item *comObject
	result = comCall(dialog, 20, uintptr(unsafe.Pointer(&item))) // IFileDialog::GetResult
	if hresultFailed(result) {
		return "", hresultError("read selected directory", result)
	}
	if item == nil {
		return "", fmt.Errorf("read selected directory: the system returned no directory")
	}
	defer comRelease(item)

	var path *uint16
	result = comCall(item, 5, sigdnFileSystemPath, uintptr(unsafe.Pointer(&path))) // IShellItem::GetDisplayName
	if hresultFailed(result) {
		return "", hresultError("read selected directory path", result)
	}
	if path == nil {
		return "", fmt.Errorf("read selected directory path: the system returned an empty path")
	}
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(path)))
	return windows.UTF16PtrToString(path), nil
}

func comCall(object *comObject, method int, args ...uintptr) uintptr {
	callArgs := make([]uintptr, 1, len(args)+1)
	callArgs[0] = uintptr(unsafe.Pointer(object))
	callArgs = append(callArgs, args...)
	result, _, _ := syscall.SyscallN(object.vtable[method], callArgs...)
	return result
}

func comRelease(object *comObject) {
	if object != nil {
		comCall(object, 2) // IUnknown::Release
	}
}

func hresultFailed(result uintptr) bool {
	return int32(uint32(result)) < 0
}

func hresultError(action string, result uintptr) error {
	return fmt.Errorf("%s: HRESULT 0x%08X", action, uint32(result))
}
