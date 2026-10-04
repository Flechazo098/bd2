package deck

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

func (s *Store) ConfigureWaypoints(load func(uint64) (gamedata.WaypointPack, error), validate func(uint64, bool) error) error {
	if load == nil || validate == nil {
		return errors.New("deck: incomplete waypoint runtime")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waypointDesign = load
	s.waypointPack = validate
	return nil
}

func (s *Store) ActivatedWaypoint(pack, id uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return hasWaypoint(s.state.Waypoints[pack], id)
}
func hasWaypoint(points []uint64, id uint64) bool {
	for _, p := range points {
		if p == id {
			return true
		}
	}
	return false
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

func (s *Store) handleWaypoint(path string, req []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(req)
	if err != nil {
		return 0, nil, true, err
	}
	pack, found, err := wire.Varint(req, 2)
	if err != nil || !found || pack == 0 || pack > math.MaxInt32 {
		return 0, nil, true, errors.New("deck: invalid waypoint pack")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waypointDesign == nil || s.waypointPack == nil {
		return 0, nil, true, errors.New("deck: waypoint runtime unavailable")
	}
	if err = s.waypointPack(pack, path == "/WaypointUse"); err != nil {
		return 0, nil, true, err
	}
	design, err := s.waypointDesign(pack)
	if err != nil {
		return 0, nil, true, err
	}
	if path == "/WaypointInfo" {
		ids := append([]uint64(nil), s.state.Waypoints[pack]...)
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		var packed []byte
		for _, id := range ids {
			if _, known := design.Points[id]; !known {
				return 0, nil, true, fmt.Errorf("deck: saved waypoint %d absent from pack%d", id, pack)
			}
			packed = binary.AppendUvarint(packed, id)
		}
		if len(packed) == 0 {
			return 31, nil, true, nil
		}
		return 31, wire.AppendBytes(nil, 1, packed), true, nil
	}
	id, found, err := wire.Varint(req, 3)
	if err != nil || !found || id == 0 || id > math.MaxInt32 {
		return 0, nil, true, errors.New("deck: invalid waypoint")
	}
	if _, known := design.Points[id]; !known {
		return 0, nil, true, errors.New("deck: unknown waypoint")
	}
	if path == "/WaypointSave" {
		if hasWaypoint(s.state.Waypoints[pack], id) {
			return 32, nil, true, nil
		}
		next := clone(s.state)
		next.Waypoints[pack] = append(next.Waypoints[pack], id)
		err = s.commit(next)
		return 32, nil, true, err
	}
	end, found, err := wire.Varint(req, 4)
	if err != nil || !found || end == 0 || end > math.MaxInt32 || end == id {
		return 0, nil, true, errors.New("deck: invalid waypoint destination")
	}
	target, known := design.Points[end]
	if !known || target.MapID == 0 || !hasWaypoint(s.state.Waypoints[pack], end) || !hasWaypoint(s.state.Waypoints[pack], id) {
		return 0, nil, true, errors.New("deck: waypoint is not activated")
	}
	moves, found, err := wire.Varint(req, 5)
	if err != nil || !found || moves != 1 {
		return 0, nil, true, errors.New("deck: invalid waypoint move count")
	}
	if reply, ok := s.cachedReplyLocked("waypoint-use", seq); ok {
		return reply.code, reply.body, true, nil
	}
	if design.PriceUnit != 0 {
		if s.wallet == nil || s.sessionID == "" {
			return 0, nil, true, errors.New("deck: waypoint wallet session unavailable")
		}
		identity := fmt.Sprintf("waypoint:%s:%d", s.sessionID, seq)
		switch design.PriceType {
		case 4:
			_, err = s.wallet.SpendGoldOnce(identity, design.PriceUnit)
		case 3:
			_, err = s.wallet.SpendFreeJewelryOnce(identity, design.PriceUnit)
		case 2:
			_, err = s.wallet.SpendJewelryOnce(identity, design.PriceUnit)
		default:
			err = errors.New("deck: unsupported waypoint currency")
		}
		if err != nil {
			return 0, nil, true, err
		}
	}
	// The client performs its warp and sends SaveUserPosition with scene coordinates.
	s.rememberReplyLocked("waypoint-use", seq, 33, nil)
	return 33, nil, true, nil
}
