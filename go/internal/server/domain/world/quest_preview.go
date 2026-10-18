package world

import (
	"encoding/binary"
	"math"

	"bd2server/internal/server/protocol/wire"
)

func (s *Service) packSubQuestClearInfo(request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 227, nil, true, ErrInvalidRequest
	}
	var packs []int
	seen := map[int]bool{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return ErrInvalidRequest
		}
		for raw := f.Value; len(raw) > 0; {
			id, n := binary.Uvarint(raw)
			if n <= 0 || id == 0 || id > math.MaxInt32 || len(packs) >= 512 {
				return ErrInvalidRequest
			}
			raw = raw[n:]
			if !seen[int(id)] {
				seen[int(id)] = true
				packs = append(packs, int(id))
			}
		}
		return nil
	})
	if err != nil {
		return 227, nil, true, err
	}
	var response []byte
	for _, id := range packs {
		if s.storyCatalog == nil {
			return 227, nil, true, ErrInvalidRequest
		}
		pack, ok := s.storyCatalog.Packs[id]
		if !ok {
			return 227, nil, true, ErrInvalidRequest
		}
		titles, err := s.presentationSource.QuestTitles(id)
		if err != nil {
			return 227, nil, true, err
		}
		var count uint64
		for _, title := range titles {
			if pack.Quests[title.LastQuest].Type == 1 && s.state.QuestCleared(title.LastQuest, id, 0) {
				count++
			}
		}
		row := wire.AppendVarint(nil, 1, uint64(id))
		row = wire.AppendVarint(row, 2, count)
		response = wire.AppendBytes(response, 1, row)
	}
	return 227, response, true, nil
}
