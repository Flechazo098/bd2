// Package iconresource generates the Windows resource object for the launcher.
package iconresource

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"bd2server/internal/common/versionconfig"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

// Generate reuses an identical object so normal builds preserve its timestamp.
func Generate(root, goos, arch string) (bool, error) {
	if goos != "windows" {
		return false, nil
	}
	architectures := map[string]winres.Arch{"amd64": winres.ArchAMD64, "386": winres.ArchI386, "arm64": winres.ArchARM64}
	target, supported := architectures[arch]
	if !supported {
		return false, fmt.Errorf("client icon: unsupported Windows architecture %q", arch)
	}
	icon, err := os.ReadFile(filepath.Join(root, "go", "internal", "client", "app", "web", "icon.ico"))
	if err != nil {
		return false, err
	}
	ico, err := winres.LoadICO(bytes.NewReader(icon))
	if err != nil {
		return false, fmt.Errorf("client icon: load ICO: %w", err)
	}
	resources := winres.ResourceSet{}
	if err := resources.SetIcon(winres.ID(3), ico); err != nil {
		return false, err
	}
	info := version.Info{}
	for _, field := range []string{version.ProductName, version.FileDescription} {
		if err := info.Set(version.LangDefault, field, "BD2 Launcher"); err != nil {
			return false, err
		}
	}
	versions, err := versionconfig.Load(filepath.Join(root, "versions.json"))
	if err != nil {
		return false, err
	}
	_, componentVersion, _ := strings.Cut(versions.ClientVersion, "+client.")
	info.SetFileVersion(componentVersion)
	info.SetProductVersion(componentVersion)
	for _, field := range []string{version.FileVersion, version.ProductVersion} {
		if err := info.Set(version.LangDefault, field, versions.ClientVersion); err != nil {
			return false, err
		}
	}
	resources.SetVersionInfo(info)
	var object bytes.Buffer
	if err := resources.WriteObject(&object, target); err != nil {
		return false, err
	}
	path := filepath.Join(root, "go", "cmd", "bd2client", "icon_windows_"+arch+".syso")
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, object.Bytes()) {
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.WriteFile(path, object.Bytes(), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
