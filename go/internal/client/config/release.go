package config

import (
	"fmt"
	"os"
	"path/filepath"

	"bd2server/internal/common/versionconfig"
)

func ReleaseVersionsBesideExecutable() (versionconfig.Config, error) {
	executable, err := os.Executable()
	if err != nil {
		return versionconfig.Config{}, fmt.Errorf("locate bd2client executable: %w", err)
	}
	return versionconfig.Load(filepath.Join(filepath.Dir(executable), versionconfig.FileName))
}
