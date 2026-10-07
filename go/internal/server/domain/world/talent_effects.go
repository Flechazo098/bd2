package world

import (
	"bd2server/internal/server/domain/command"
	"fmt"
)

func (s *Service) AttachTalentPackInfo(source func(ctx command.Context, _ int) ([]byte, error)) {
	s.talentPackInfo = source
}

func (s *Service) TalentFieldContext(ctx command.Context) (int, uint64, bool, error) {
	pack, err := s.CurrentPackID(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	mapID, err := s.currentFieldMap(ctx, pack)
	if err != nil {
		return 0, 0, false, err
	}
	battle := s.battleActive != nil && s.battleActive(ctx)
	return pack, uint64(mapID), battle, nil
}

// Hidden packs deliberately suppress SaveUserPosition, so a calendar-authored
// initial map is authoritative until the client supplies a valid saved map.
// Ordinary pack starts live in GateSpotData assets, not QuestTable.MapId;
// guessing a quest target map would authorize interactions in another scene.
func (s *Service) currentFieldMap(ctx command.Context, pack int) (int, error) {
	if !s.packUnlocked(ctx, pack) {
		return 0, ErrInvalidRequest
	}
	if saved, ok := s.state.Position(); ok && saved.PackID == pack && saved.Difficulty == s.questDifficulty(pack) && saved.Position.MapID > 0 {
		return saved.Position.MapID, nil
	}
	if event, found, e := s.resolveEventFieldPack(ctx, pack); e != nil {
		return 0, e
	} else if found && event.InitialMapID > 0 {
		return int(event.InitialMapID), nil
	}
	return 0, fmt.Errorf("world: current scene map is not yet known")
}
