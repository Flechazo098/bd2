package world

import "bd2server/internal/server/wire"

func (s *Service) fieldBuffInfo() ([]byte, error) {
	rows, err := s.state.FieldBuffs()
	if err != nil {
		return nil, err
	}
	now := s.monsterTime().UnixMilli()
	var out []byte
	for _, row := range rows {
		end, _, err := wire.Varint(row, 3)
		if err != nil {
			return nil, err
		}
		if end != 0 && (end > 0x7fffffffffffffff || int64(end) <= now) {
			continue
		}
		out = wire.AppendBytes(out, 8, row)
	}
	return out, nil
}
