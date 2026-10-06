package world

import (
	"bd2server/internal/server/progress"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

func (s *Service) handleFieldObjectPosition(request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 95, nil, true, err
	}
	group, _, err := wire.Varint(request, 3)
	if err != nil || group == 0 || group > 0x7fffffff {
		return 95, nil, true, ErrInvalidRequest
	}
	id, _, err := wire.Varint(request, 4)
	if err != nil || id == 0 || id > 0x7fffffff {
		return 95, nil, true, ErrInvalidRequest
	}
	position, found, err := wire.Bytes(request, 5)
	if err != nil || !found {
		return 95, nil, true, ErrInvalidRequest
	}
	mapID, _, err := wire.Varint(position, 1)
	if err != nil || mapID == 0 || mapID > 0x7fffffff {
		return 95, nil, true, ErrInvalidRequest
	}
	if err = wire.Walk(position, func(f wire.Field) error {
		if f.Number >= 2 && f.Number <= 4 {
			if f.Type != 5 {
				return ErrInvalidRequest
			}
			value := math.Float32frombits(binary.LittleEndian.Uint32(f.Value))
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return ErrInvalidRequest
			}
		}
		return nil
	}); err != nil {
		return 95, nil, true, err
	}
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return 95, nil, true, err
	}
	obj, exists := design.Actions[int(id)]
	if !exists || obj.GroupID != int(group) || obj.Type != 1 || !s.packUnlocked(pack) || !s.fieldObjectCurrentPack(pack) {
		return 95, nil, true, fmt.Errorf("%w: unavailable field action object", ErrInvalidRequest)
	}
	currentMap, err := s.currentFieldMap(pack)
	if err != nil || currentMap != int(mapID) {
		return 95, nil, true, fmt.Errorf("%w: field action outside current map", ErrInvalidRequest)
	}
	cleared := obj.QuestID > 0 && s.state.QuestCleared(obj.QuestID, pack, s.questDifficulty(pack))
	if obj.QuestID > 0 && (obj.QuestEnableType == 1 && !cleared || obj.QuestEnableType == 2 && cleared) {
		return 95, nil, true, fmt.Errorf("%w: field action unavailable for quest", ErrInvalidRequest)
	}
	if err = s.state.SaveFieldActionPosition(pack, int(id), progress.FieldActionPosition{Position: position, QuestCleared: cleared}); err != nil {
		return 95, nil, true, err
	}
	return 95, []byte{}, true, nil
}

func (s *Service) fieldActionInfo(pack int) ([]byte, error) {
	positions, err := s.state.FieldActionPositions(pack)
	if err != nil || len(positions) == 0 {
		return nil, err
	}
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(positions))
	for id := range positions {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var out []byte
	for _, id := range ids {
		obj, found := design.Actions[id]
		if !found {
			return nil, fmt.Errorf("world: saved field action absent from design")
		}
		stored := positions[id]
		// Client resets authored action positions when the related quest ends.
		// Omit that stale position on reconnect so the prefab uses its origin.
		if obj.QuestID > 0 && stored.QuestCleared != s.state.QuestCleared(obj.QuestID, pack, s.questDifficulty(pack)) {
			continue
		}
		info := wire.AppendBytes(wire.AppendVarint(nil, 1, uint64(id)), 2, stored.Position)
		out = wire.AppendBytes(out, 2, info)
	}
	return out, nil
}
