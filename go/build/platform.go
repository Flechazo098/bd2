package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type target struct{ platform, goos, architecture, goarch, goarm string }

func nativeTarget() (target, error) {
	arch := runtime.GOARCH
	if runtime.GOOS == "windows" {
		machine := os.Getenv("PROCESSOR_ARCHITEW6432")
		if machine == "" {
			machine = os.Getenv("PROCESSOR_ARCHITECTURE")
		}
		switch strings.ToLower(machine) {
		case "amd64":
			arch = "amd64"
		case "arm64":
			arch = "arm64"
		case "x86":
			arch = "386"
		}
	} else {
		if out, err := exec.Command("uname", "-m").Output(); err == nil {
			arch = strings.TrimSpace(string(out))
		}
	}
	return platformTarget(runtime.GOOS, arch)
}

func platformTarget(goos, arch string) (target, error) {
	t := target{goos: goos}
	switch goos {
	case "windows", "linux":
		t.platform = goos
	case "darwin":
		t.platform = "macos"
	default:
		return t, fmt.Errorf("unsupported platform %q", goos)
	}
	switch strings.ToLower(arch) {
	case "amd64", "x86_64", "x64":
		t.architecture = "x64"
		t.goarch = "amd64"
	case "aarch64", "arm64":
		t.architecture = "arm64"
		t.goarch = "arm64"
	case "386", "i386", "i486", "i586", "i686", "x86":
		t.architecture = "x86"
		t.goarch = "386"
	case "arm", "armv7", "armv7l":
		t.architecture = "armv7"
		t.goarch = "arm"
		t.goarm = "7"
	default:
		return t, fmt.Errorf("unsupported architecture %q", arch)
	}
	return t, nil
}
