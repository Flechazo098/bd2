package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
)

// EventFieldPackSource binds installed hidden packs to the server's current
// calendars. A purchase receipt alone never authorizes an expired event.
type EventFieldPackSource interface {
	ResolveEventFieldPack(ctx command.Context, _ int) (gamedata.EventFieldPack, bool, error)
	ListEventFieldPacks(ctx command.Context) ([]gamedata.EventFieldPack, error)
}

// PackAvailable exposes the same account/calendar authorization to NPC
// services without letting them infer access from the client's pack number.
func (s *Service) PackAvailable(ctx command.Context, id uint64) bool {
	if id == 0 || id > uint64(^uint32(0)>>1) {
		return false
	}
	return s.packUnlocked(ctx, int(id))
}

func (s *Service) AttachEventFieldPacks(source EventFieldPackSource) error {
	if source == nil {
		return fmt.Errorf("world: nil event field pack source")
	}
	s.eventFieldPacks = source
	return nil
}

func (s *Service) resolveEventFieldPack(ctx command.Context, id int) (gamedata.EventFieldPack, bool, error) {
	if s.eventFieldPacks == nil {
		return gamedata.EventFieldPack{}, false, nil
	}
	return s.eventFieldPacks.ResolveEventFieldPack(ctx, id)
}

func (s *Service) eventPackPurchased(id int) bool {
	if s.collection == nil {
		return false
	}
	_, found := s.collection.Grant(packPurchaseIdentity(id))
	return found
}
