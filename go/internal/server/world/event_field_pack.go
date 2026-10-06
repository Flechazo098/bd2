package world

import (
	"fmt"
	"slices"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

// EventFieldPackSource binds installed hidden packs to the server's current
// calendars. A purchase receipt alone never authorizes an expired event.
type EventFieldPackSource interface {
	ResolveEventFieldPack(int) (gamedata.EventFieldPack, bool, error)
	ListEventFieldPacks() ([]gamedata.EventFieldPack, error)
}

// PackAvailable exposes the same account/calendar authorization to NPC
// services without letting them infer access from the client's pack number.
func (s *Service) PackAvailable(id uint64) bool {
	if id == 0 || id > uint64(^uint32(0)>>1) {
		return false
	}
	return s.packUnlocked(int(id))
}

func (s *Service) AttachEventFieldPacks(source EventFieldPackSource) error {
	if source == nil {
		return fmt.Errorf("world: nil event field pack source")
	}
	s.eventFieldPacks = source
	return nil
}

func (s *Service) resolveEventFieldPack(id int) (gamedata.EventFieldPack, bool, error) {
	if s.eventFieldPacks == nil {
		return gamedata.EventFieldPack{}, false, nil
	}
	return s.eventFieldPacks.ResolveEventFieldPack(id)
}

func (s *Service) eventPackPurchased(id int) bool {
	if s.collection == nil {
		return false
	}
	_, found := s.collection.Grant(packPurchaseIdentity(id))
	return found
}

func (s *Service) eventPackDBInfo(pack gamedata.EventFieldPack) []byte {
	row := wire.AppendVarint(nil, 1, uint64(pack.ID))
	if s.eventPackPurchased(pack.ID) {
		row = wire.AppendVarint(row, 8, 1)
	}
	return row
}

func (s *Service) eventPackInfoRows() ([][]byte, error) {
	if s.eventFieldPacks == nil {
		return nil, nil
	}
	packs, err := s.eventFieldPacks.ListEventFieldPacks()
	if err != nil {
		return nil, err
	}
	var rows [][]byte
	for _, pack := range packs {
		// PackManager buys a hidden pack only when PackInfo has no row for it.
		if s.eventPackPurchased(pack.ID) {
			rows = append(rows, s.eventPackDBInfo(pack))
		}
	}
	return rows, nil
}

func (s *Service) enterEventFieldPack(pack gamedata.EventFieldPack) (int, []byte, bool, error) {
	if !s.eventPackPurchased(pack.ID) {
		return 0, nil, true, fmt.Errorf("%w: event pack %d is not purchased", ErrInvalidRequest, pack.ID)
	}
	position := pack.InitialPosition
	if position == "" {
		return 0, nil, true, fmt.Errorf("world: missing event pack initial position")
	}
	if saved, found := s.state.Position(); found && saved.PackID == pack.ID {
		if !slices.Contains(pack.MapIDs, saved.Position.MapID) {
			return 0, nil, true, fmt.Errorf("%w: map outside event pack", ErrInvalidRequest)
		}
		position = saved.RawJSON
	}
	response := wire.AppendString(nil, 4, position)
	buffs, err := s.fieldBuffInfo()
	if err != nil {
		return 0, nil, true, err
	}
	response = append(response, buffs...)
	// The common callback dereferences HuntingGroundInfo even in hidden packs.
	// Use the domain-generated empty/current snapshot; never borrow the outside
	// map's monsters or story progress.
	var hunting []byte
	if s.huntingGround != nil {
		var err error
		hunting, err = s.huntingGround.EnsureForPack(pack.ID)
		if err != nil {
			return 0, nil, true, err
		}
	}
	response = wire.AppendBytes(response, 12, hunting)
	// Hidden-pack entry must retain the outside field position: the client
	// intentionally suppresses SaveUserPosition while playing these packs. The
	// persistent active pack also stays outside, so relogin cannot be stranded
	// in a hidden scene after its calendar closes.
	s.setCurrentPack(pack.ID)
	return 5, response, true, nil
}
