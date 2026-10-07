package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
)

// AttachWaypointRuntime must follow AttachDecks; Save permits the client's
// automatic activation of all safe-area waypoints when it enters a pack.
func (s *Service) AttachWaypointRuntime(ctx command.Context, source *gamedata.Source) error {
	if s.decks == nil {
		return fmt.Errorf("world: waypoint deck unavailable")
	}
	return s.decks.ConfigureWaypoints(ctx, func(pack uint64) (gamedata.WaypointPack, error) {
		return source.Waypoint(pack)
	}, func(ctx command.Context, pack uint64, use bool) error {
		if !s.packUnlocked(ctx, int(pack)) {
			return fmt.Errorf("world: waypoint pack%d locked", pack)
		}
		if use {
			current, err := s.CurrentPackID(ctx)
			if err != nil {
				return err
			}
			if uint64(current) != pack {
				return fmt.Errorf("world: waypoint pack is not current")
			}
			if s.battleActive != nil && s.battleActive(ctx) {
				return fmt.Errorf("world: waypoint travel during battle")
			}
		}
		return nil
	})
}
