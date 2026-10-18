package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"fmt"
)

func (s *Service) ApplyTalentWaypoint(ctx command.Context, _ string, _ roster.Character, rule gamedata.TalentUseRule, targets []uint64) ([]byte, error) {
	pack, _, _, err := s.TalentFieldContext(ctx)
	if err != nil {
		return nil, err
	}
	if rule.Class != 15 || len(targets) != 1 {
		return nil, fmt.Errorf("world: waypoint talent requires one destination map")
	}
	design, err := s.presentationSource.Waypoint(uint64(pack))
	if err != nil {
		return nil, err
	}
	for _, point := range design.Points {
		if point.MapID == targets[0] && s.decks.ActivatedWaypoint(uint64(pack), point.ID) && (len(rule.Values) > 0 && rule.Values[0] != 0 || point.SafeArea) {
			return nil, nil
		}
	}
	return nil, fmt.Errorf("world: waypoint talent destination unavailable pack=%d map=%d level=%d", pack, targets[0], rule.Level)
}

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
	if mapID, found, err := s.skyWayMap(ctx, pack); found || err != nil {
		return mapID, err
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
