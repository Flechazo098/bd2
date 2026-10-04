package world

import (
	"errors"

	"bd2server/internal/server/gamedata"
)

// LastPlayedPackID prevents LoginUser from selecting a field that this server
// cannot restore. A fresh account keeps the versioned login seed's pack.
func (s *Service) LastPlayedPackID() (uint64, error) {
	id := uint64(s.state.ActivePackID())
	if id == 0 {
		var err error
		id, err = s.state.LastPlayedPackID()
		if err != nil {
			return id, err
		}
	}
	if id == 0 {
		id = uint64(s.startingPack())
	}
	if !s.packUnlocked(int(id)) {
		return 0, errors.New("world: saved login pack is unavailable")
	}
	if pack, field := s.fieldPacks[int(id)]; field {
		saved, _ := s.state.Position()
		if !pack.MapIDs[saved.Position.MapID] {
			return 0, errors.New("world: saved arena map does not belong to pack")
		}
	}
	return id, nil
}

func (s *Service) AttachSquadLevel(provider func() (uint64, error)) error {
	if provider == nil {
		return errors.New("world: nil squad level provider")
	}
	s.squadLevel = provider
	return nil
}

func (s *Service) fieldPackUnlocked(pack gamedata.FieldPack) bool {
	// A valid committed field position is the existing account's entry marker.
	// Restoration does not repeat a purchase or an unlock check.
	if saved, found := s.state.Position(); found && saved.PackID == int(pack.ID) && pack.MapIDs[saved.Position.MapID] {
		return true
	}
	// Scheduled arena availability needs its own persisted event domain.
	if pack.UseSchedule != 0 {
		return false
	}
	if pack.SquadLevel != 0 {
		if s.squadLevel == nil {
			return false
		}
		level, err := s.squadLevel()
		if err != nil || level < pack.SquadLevel {
			return false
		}
	}
	if pack.HasOpenRule {
		if s.inventory == nil {
			return false
		}
		found := false
		for _, item := range s.inventory.All() {
			if item.Type == 19 && item.ID == pack.TicketID && item.Count != 0 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// Paid packs require a durable purchase marker. An existing saved position
	// proves this account already entered it; this does not implement purchase.
	if pack.BuyPrice != 0 {
		return false
	}
	return true
}
