//go:build windows

package app

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	platformUser32DLL            = syscall.NewLazyDLL("user32.dll")
	messageBoxW                  = platformUser32DLL.NewProc("MessageBoxW")
	enumWindowsProc              = platformUser32DLL.NewProc("EnumWindows")
	getWindowThreadProcessIDProc = platformUser32DLL.NewProc("GetWindowThreadProcessId")
	isWindowVisibleProc          = platformUser32DLL.NewProc("IsWindowVisible")
	isIconicProc                 = platformUser32DLL.NewProc("IsIconic")
	showWindowAsyncProc          = platformUser32DLL.NewProc("ShowWindowAsync")
	setForegroundWindowProc      = platformUser32DLL.NewProc("SetForegroundWindow")
)

// ShowFatalError keeps startup failures visible even though the release
// executable uses the Windows GUI subsystem and therefore has no console.
func ShowFatalError(err error) {
	if err == nil {
		return
	}
	message, conversionErr := syscall.UTF16PtrFromString(fmt.Sprintf(
		"BD2 Client Studio could not start:\n\n%s\n\nSee the logs directory next to bd2client.exe for details.", err,
	))
	if conversionErr != nil {
		return
	}
	title, conversionErr := syscall.UTF16PtrFromString("BD2 Client Studio")
	if conversionErr != nil {
		return
	}
	if result, _, callErr := messageBoxW.Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10); result == 0 {
		log.Printf("Fatal error dialog could not be displayed: %v", callErr)
	}
}

// CREATE_NO_WINDOW prevents console-subsystem helpers such as powershell.exe
// from allocating a visible console when bd2client is built as a Windows GUI
// executable. HideWindow also covers helpers that elect to create a window
// despite inheriting no console from the parent process.
const createNoWindow = 0x08000000

// visibleCommand suppresses a console allocation without hiding the GUI
// window created by the child process.
func visibleCommand(name string, args ...string) *exec.Cmd {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return command
}

func launchGame(target, proxyURL string) error {
	if processID, running, err := windowsExecutableProcessID(filepath.Base(target)); err != nil {
		return err
	} else if running {
		if !activateProcessWindow(processID, 5*time.Second) {
			return fmt.Errorf("Brown Dust II is running, but its window could not be restored") //nolint:staticcheck // ST1005
		}
		return errGameAlreadyRunning
	}
	command := visibleCommand(target, gameLaunchArguments()...)
	command.Dir = filepath.Dir(target)
	command.Env = gameProxyEnvironment(os.Environ(), proxyURL)
	if err := command.Start(); err != nil {
		return err
	}
	// Unity creates the top-level window asynchronously. Best-effort foreground
	// activation prevents the new window from opening behind Client Studio.
	activateProcessWindow(uint32(command.Process.Pid), 15*time.Second)
	return nil
}

func windowsExecutableProcessID(name string) (uint32, bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		if err := windows.CloseHandle(snapshot); err != nil {
			log.Printf("Process snapshot cleanup failed: %v", err)
		}
	}()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0, false, err
	}
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), name) {
			return entry.ProcessID, true, nil
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return 0, false, nil
			}
			return 0, false, err
		}
	}
}

func activateProcessWindow(processID uint32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if window := topLevelWindowForProcess(processID); window != 0 {
			iconic, _, _ := isIconicProc.Call(window)
			if iconic != 0 {
				const swRestore = 9
				if result, _, callErr := showWindowAsyncProc.Call(window, swRestore); result == 0 {
					log.Printf("Game window restore request failed: %v", callErr)
				}
			}
			if result, _, _ := setForegroundWindowProc.Call(window); result == 0 {
				// Windows may deny foreground activation even for a valid game window.
				log.Print("Windows declined foreground activation of the game window")
			}
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func topLevelWindowForProcess(processID uint32) uintptr {
	var found uintptr
	callback := syscall.NewCallback(func(window uintptr, _ uintptr) uintptr {
		var owner uint32
		if thread, _, _ := getWindowThreadProcessIDProc.Call(window, uintptr(unsafe.Pointer(&owner))); thread == 0 {
			return 1 // The window disappeared during enumeration.
		}
		visible, _, _ := isWindowVisibleProc.Call(window)
		if owner == processID && visible != 0 {
			found = window
			return 0
		}
		return 1
	})
	if result, _, callErr := enumWindowsProc.Call(callback, 0); result == 0 && found == 0 {
		// A successful match deliberately stops enumeration and also returns zero.
		if callErr != syscall.Errno(0) {
			log.Printf("Game window enumeration failed: %v", callErr)
		}
	}
	return found
}
