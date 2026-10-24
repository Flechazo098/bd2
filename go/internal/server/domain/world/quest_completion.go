package world

import (
	"fmt"
	"slices"

	"bd2server/internal/server/protocol/wire"
)

func (s *Service) questMaxClearInfo(request []byte) (int, []byte, bool, error) {
	if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
		return 137, nil, true, fmt.Errorf("%w: QuestMaxClearInfo sequence %d (present=%t): %v", ErrInvalidRequest, seq, found, err)
	}
	maxima := make(map[int]int, len(s.packs))
	for pack := range s.packs {
		maxima[pack] = 0
	}
	if s.storyCatalog != nil {
		for pack := range s.storyCatalog.Packs {
			maxima[pack] = 0
		}
	}
	for _, quest := range s.state.AllClearedQuests() {
		maxima[quest.PackID] = max(maxima[quest.PackID], quest.QuestID)
	}
	ids := make([]int, 0, len(maxima))
	for id := range maxima {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out []byte
	for _, pack := range ids {
		row := wire.AppendVarint(nil, 1, uint64(pack))
		if maxima[pack] != 0 {
			row = wire.AppendVarint(row, 2, uint64(maxima[pack]))
		}
		out = wire.AppendBytes(out, 1, row)
	}
	return 137, out, true, nil
}

func (s *Service) questTitleInfo(pack int) ([][]byte, error) {
	if s.storyCatalog == nil {
		return nil, nil
	}
	if _, found := s.storyCatalog.Packs[pack]; !found {
		return nil, nil
	}
	titles, err := s.presentationSource.QuestTitles(pack)
	if err != nil {
		return nil, err
	}
	cleared := s.state.AllClearedQuests()
	var rows [][]byte
	for _, title := range titles {
		highest := 0
		for _, quest := range cleared {
			if quest.PackID == pack && quest.QuestID >= title.FirstQuest && quest.QuestID <= title.LastQuest {
				highest = max(highest, quest.QuestID)
			}
		}
		row := wire.AppendVarint(nil, 1, uint64(title.ID))
		if highest != 0 {
			row = wire.AppendVarint(row, 2, uint64(highest))
		}
		rows = append(rows, row)
	}
	return rows, nil
}
