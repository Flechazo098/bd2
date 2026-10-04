package world

import (
	"bd2server/internal/server/gamedata"
	"fmt"
)

// AttachWaypointRuntime must follow AttachDecks; Save permits the client's
// automatic activation of all safe-area waypoints when it enters a pack.
func (s *Service) AttachWaypointRuntime(root, version string) error {
	if s.decks == nil {
		return fmt.Errorf("world: waypoint deck unavailable")
	}
	return s.decks.ConfigureWaypoints(func(pack uint64) (gamedata.WaypointPack, error) {
		return gamedata.LoadWaypointPack(root, version, pack)
	}, func(pack uint64, use bool) error {
		if !s.packUnlocked(int(pack)) {
			return fmt.Errorf("world: waypoint pack%d locked", pack)
		}
		if use {
			current, err := s.CurrentPackID()
			if err != nil {
				return err
			}
			if uint64(current) != pack {
				return fmt.Errorf("world: waypoint pack is not current")
			}
			if s.battleActive != nil && s.battleActive() {
				return fmt.Errorf("world: waypoint travel during battle")
			}
		}
		return nil
	})
}
