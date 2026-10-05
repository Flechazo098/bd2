package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
)

func (s *Service) AttachTalentPackInfo(source func(int) ([]byte, error)) { s.talentPackInfo = source }

func (s *Service) TalentFieldContext() (int, uint64, bool, error) {
	pack, err := s.CurrentPackID()
	if err != nil {
		return 0, 0, false, err
	}
	mapID, err := s.currentFieldMap(pack)
	if err != nil {
		return 0, 0, false, err
	}
	battle := s.battleActive != nil && s.battleActive()
	return pack, uint64(mapID), battle, nil
}

// Hidden packs deliberately suppress SaveUserPosition, so a calendar-authored
// initial map is authoritative until the client supplies a valid saved map.
// Ordinary pack starts live in GateSpotData assets, not QuestTable.MapId;
// guessing a quest target map would authorize interactions in another scene.
func (s *Service) currentFieldMap(pack int) (int, error) {
	if !s.packUnlocked(pack) {
		return 0, ErrInvalidRequest
	}
	if saved, ok := s.state.Position(); ok && saved.PackID == pack && saved.Difficulty == s.questDifficulty(pack) && saved.Position.MapID > 0 {
		return saved.Position.MapID, nil
	}
	if event, found, e := s.resolveEventFieldPack(pack); e != nil {
		return 0, e
	} else if found && event.InitialMapID > 0 {
		return int(event.InitialMapID), nil
	}
	return 0, fmt.Errorf("world: current scene map is not yet known")
}

// Absorb consumes the exact acquisition objects requested by the client using
// the same period receipts and reward graph as an ordinary field interaction.
func (s *Service) ApplyTalentFieldAbsorb(_ string, _ player.Character, rule gamedata.TalentUseRule, ids []uint64) ([]byte, error) {
	pack, mapID, _, err := s.TalentFieldContext()
	if err != nil {
		return nil, err
	}
	if rule.Class != 4 || len(ids) == 0 {
		return nil, ErrInvalidRequest
	}
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, id := range ids {
		obj, ok := design.Objects[int(id)]
		if !ok || obj.MapID != int(mapID) || (obj.Type != 1 && obj.Type != 3) {
			return nil, fmt.Errorf("world: absorption target is not an acquisition object")
		}
		bundle, e := s.openFieldObject(pack, obj.GroupID, int(id))
		if e != nil {
			return nil, e
		}
		e = wire.Walk(bundle, func(f wire.Field) error {
			to := map[int]int{1: 3, 2: 5, 3: 6, 4: 4}[f.Number]
			if to > 0 {
				out = wire.AppendBytes(out, to, f.Value)
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
