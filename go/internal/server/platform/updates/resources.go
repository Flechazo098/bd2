package updates

import (
	"bd2server/internal/common/release"
	"path/filepath"
)

func ResourceDirectory(dataDir string, g release.GameResources) string {
	return filepath.Join(dataDir, "resources", "pinned", g.ResourceID())
}
