package eventactions

import (
	"bd2server/internal/server/design/gamedata"
	"fmt"
	"time"
)

// Spawn slots repeat daily in the same server UTC clock used by the game's
// schedule response. Preparation time is client presentation; Start must fall
// inside the actual playing interval declared by FieldSpawnEventTable.
func (s *Service) spawnWindow(row gamedata.EventActionRow) error {
	start, err := time.Parse("15:04:05", row.Text[7])
	if err != nil {
		return fmt.Errorf("eventactions: malformed spawn start time")
	}
	end, err := time.Parse("15:04:05", row.Text[1])
	if err != nil {
		return fmt.Errorf("eventactions: malformed spawn end time")
	}
	seconds := func(t time.Time) int { return t.Hour()*3600 + t.Minute()*60 + t.Second() }
	from, to, now := seconds(start), seconds(end), seconds(s.now().UTC())
	available := now >= from && now < to
	if to < from {
		available = now >= from || now < to
	}
	if !available {
		return fmt.Errorf("eventactions: spawn slot not playing")
	}
	return nil
}
