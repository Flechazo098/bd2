package main

import "path/filepath"

func resolveDevelopmentSettingsPath(dataDir, override string) string {
	if override != "" {
		return filepath.Clean(override)
	}
	return filepath.Join(dataDir, "dev-tools.json")
}
