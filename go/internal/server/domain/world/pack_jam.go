package world

import (
	"fmt"
)

func packJamIdentity(packID int) string { return fmt.Sprintf("pack-jam:%d", packID) }
