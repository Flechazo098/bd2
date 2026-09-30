//go:build windows

package app

import "testing"

func TestHiddenCommandNeverAllocatesVisibleConsole(t *testing.T) {
	command := hiddenCommand("powershell.exe", "-NoProfile")
	if command.SysProcAttr == nil {
		t.Fatal("hidden command has no Windows process attributes")
	}
	if !command.SysProcAttr.HideWindow {
		t.Fatal("hidden command does not request a hidden window")
	}
	if command.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("hidden command creation flags %#x omit CREATE_NO_WINDOW", command.SysProcAttr.CreationFlags)
	}
}

func TestVisibleCommandDoesNotHideGUIWindow(t *testing.T) {
	command := visibleCommand("Brown Dust II.exe")
	if command.SysProcAttr == nil {
		t.Fatal("visible command has no Windows process attributes")
	}
	if command.SysProcAttr.HideWindow {
		t.Fatal("visible game command requests a hidden window")
	}
	if command.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("visible command creation flags %#x omit CREATE_NO_WINDOW", command.SysProcAttr.CreationFlags)
	}
}

func TestHRESULTFailureClassification(t *testing.T) {
	if hresultFailed(0) || hresultFailed(1) {
		t.Fatal("successful HRESULT classified as failure")
	}
	if !hresultFailed(errorCancelled) || !hresultFailed(0x80004005) {
		t.Fatal("failed HRESULT classified as success")
	}
}
