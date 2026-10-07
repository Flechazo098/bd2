package deck

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"errors"
	"math"
	"slices"
)

func (s *Store) ConfigureWaypoints(ctx command.Context, load func(uint64) (gamedata.WaypointPack, error), validate func(ctx command.Context, _ uint64, _ bool) error) error {
	if load == nil || validate == nil {
		return errors.New("deck: incomplete waypoint runtime")
	}

	s.waypointDesign = load
	s.waypointPack = validate
	return nil
}

func (s *Store) ActivatedWaypoint(pack, id uint64) bool {

	return hasWaypoint(s.state.Waypoints[pack], id)
}
func hasWaypoint(points []uint64, id uint64) bool {
	return slices.Contains(points, id)
}

func validWaypointState(packs map[uint64][]uint64) error {
	for pack, points := range packs {
		if pack == 0 || pack > math.MaxInt32 || points == nil {
			return errors.New("deck: invalid saved waypoint pack")
		}
		seen := map[uint64]bool{}
		for _, id := range points {
			if id == 0 || id > math.MaxInt32 || seen[id] {
				return errors.New("deck: invalid saved waypoint activation")
			}
			seen[id] = true
		}
	}
	return nil
}
