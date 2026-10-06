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
		ids, err := s.openedFieldObjects(int(id))
		if err != nil {
			return 0, nil, true, err
		}
		var once, regen uint64
		for _, objectID := range ids {
			object := s.fieldObjects[int(id)].Objects[objectID]
			if object.Type == 2 { //nolint:staticcheck // QF1003
				once++
			} else if object.Type == 1 || object.Type == 3 {
				regen++
			}
		}
		row := wire.AppendVarint(nil, 1, id)
		if once > 0 {
			row = wire.AppendVarint(row, 2, once)
		}
		if regen > 0 {
			row = wire.AppendVarint(row, 3, regen)
		}
		if s.packDetailDesign != nil && s.monsterLoader != nil {
			design, e := s.packDetailDesign(int(id))
			if e != nil {
				return 0, nil, true, e
			}
			filter := map[int]bool{}
			for _, n := range design.RegenMonsterIDs {
				filter[n] = true
			}
			rows, e := s.monsterRows(int(id), filter)
			if e != nil {
				return 0, nil, true, e
			}
			var defeated uint64
			for _, m := range rows {
				active, _, _ := wire.Varint(m, 6)
				respawn, _, _ := wire.Varint(m, 3)
				if active == 0 || respawn > uint64(s.monsterTime().UnixMilli()) {
					defeated++
				}
			}
			if defeated > 0 {
				row = wire.AppendVarint(row, 4, defeated)
			}
		}
		research, e := s.state.ResearchObjects(int(id))
		if e != nil {
			return 0, nil, true, e
		}
		if len(research) > 0 {
			row = wire.AppendVarint(row, 5, uint64(len(research)))
		}
		response = wire.AppendBytes(response, 1, row)
	}
	return 625, response, true, nil
}
