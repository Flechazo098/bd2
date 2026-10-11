package gamedata

import (
	"database/sql"
	"fmt"
	"time"

	"bd2server/internal/server/protocol/wire"
)

type MissionWindow struct{ Start, End time.Time }

func loadMissionWindows(db *sql.DB, design *MissionDesign) error {
	rows, err := db.Query("SELECT groupId,id,ProtoBuf FROM ChangeMissionTable")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	design.Windows = map[[2]uint64]MissionWindow{}
	for rows.Next() {
		var group, id uint64
		var raw []byte
		if err := rows.Scan(&group, &id, &raw); err != nil {
			return err
		}
		var window MissionWindow
		for field, target := range map[int]*time.Time{1: &window.End, 4: &window.Start} {
			text, found, err := wire.Bytes(raw, field)
			if err != nil || !found {
				return fmt.Errorf("gamedata: missing mission window %d/%d", group, id)
			}
			*target, err = time.Parse("2006-01-02", string(text))
			if err != nil {
				return fmt.Errorf("gamedata: invalid mission window %d/%d: %w", group, id, err)
			}
		}
		key := [2]uint64{group, id}
		if _, exists := design.Windows[key]; exists {
			return fmt.Errorf("gamedata: duplicate mission window %d/%d", group, id)
		}
		design.Windows[key] = window
	}
	return rows.Err()
}
