package pictorial

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/PictorialBookInfo" && path != "/AllCharRefresh" {
		return 0, nil, false, nil
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, fmt.Errorf("pictorial: %s missing sequence", path)
	}
	entries, buffs, err := s.Snapshot(ctx)
	if err != nil {
		return 0, nil, true, err
	}
	var response []byte
	if path == "/PictorialBookInfo" {
		for _, entry := range entries {
			book := wire.AppendVarint(nil, 1, entry.GroupID)
			book = wire.AppendVarint(book, 2, entry.ID)
			response = wire.AppendBytes(response, 1, book)
		}
		return 111, response, true, nil
	}
	for _, buff := range buffs {
		response = wire.AppendBytes(response, 1, BuffWire(buff))
	}
	return 165, response, true, nil
}

func BuffWire(buff gamedata.PictorialBuffStat) []byte {
	proto := wire.AppendVarint(nil, 1, buff.StatType)
	proto = wire.AppendDouble(proto, 2, buff.Value)
	if buff.Category != 0 {
		proto = wire.AppendVarint(proto, 3, buff.Category)
	}
	return proto
}
