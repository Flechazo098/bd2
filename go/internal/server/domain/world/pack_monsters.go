package world

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

func (s *Service) packMonsterRows(ctx command.Context, pack int) ([][]byte, error) {
	if s.packDetailDesign == nil {
		return nil, fmt.Errorf("world: missing pack detail design loader")
	}
	design, err := s.packDetailDesign(pack)
	if err != nil {
		return nil, err
	}
	filter := make(map[int]bool, len(design.RegenMonsterIDs))
	for _, id := range design.RegenMonsterIDs {
		filter[id] = true
	}
	return s.monsterRows(ctx, pack, filter)
}

func (s *Service) packMonsterDefeatedCount(ctx command.Context, pack int) (uint64, error) {
	rows, err := s.packMonsterRows(ctx, pack)
	if err != nil {
		return 0, err
	}
	var count uint64
	for _, row := range rows {
		active, _, err := wire.Varint(row, 6)
		if err != nil {
			return 0, err
		}
		respawn, _, err := wire.Varint(row, 3)
		if err != nil {
			return 0, err
		}
		if active == 0 || respawn > uint64(s.monsterTime().UnixMilli()) {
			count++
		}
	}
	return count, nil
}
