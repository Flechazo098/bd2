package world

import (
	"bd2server/internal/server/wire"
	"fmt"
)

func (s *Service) handlePackSummary(request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return 0, nil, true, fmt.Errorf("%w: PackSummaryInfoList missing or invalid sequence", ErrInvalidRequest)
	}
	var response []byte
	for _, row := range s.packDBInfoRows() {
		id, found, err := wire.Varint(row, 1)
		if err != nil || !found {
			return 0, nil, true, fmt.Errorf("world: invalid account pack row")
		}
		if !s.packSummaryTargets[int(id)] {
			continue
		}
		// These are collected reward counts, not quest completion or remaining
		// rewards. Field object and research grants are not implemented. The
		// client's monster category requires UseBattleSkip && Type != 3 &&
		// RegenId > 0; the current story battles (pack21 monsters 1..4 and
		// pack22 monsters 1..8) have both RegenId and UseBattleSkip zero and
		// their persisted battle reward grants do not count in this category.
		// Regenerating field reward battles are not implemented. The collected
		// counts are therefore zero (protobuf defaults). The client loads the
		// maxima from PackRewardSummaryData and calculates the remainder.
		response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 1, id))
	}
	return 625, response, true, nil
}
