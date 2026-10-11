package app

import (
	"encoding/binary"
	"errors"
	"math"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/protocol/wire"
)

type friendSupporterDetailHandler struct {
	owner func(command.Context) (uint64, error)
	load  func(command.Context, uint64) (map[uint64][]byte, error)
}

func (h friendSupporterDetailHandler) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/SupporterDetail" {
		return 0, nil, false, nil
	}
	var fields [4]uint64
	var seen [4]bool
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number < 1 || f.Number > 3 {
			return nil
		}
		if f.Type != 0 || seen[f.Number] {
			return wire.ErrMalformed
		}
		fields[f.Number], _ = binary.Uvarint(f.Value)
		seen[f.Number] = true
		return nil
	})
	if err != nil {
		return 434, nil, true, err
	}
	if fields[1] == 0 || fields[1] > math.MaxInt32 || fields[2] == 0 || fields[2] > math.MaxInt64 || fields[3] > 1 {
		return 434, nil, true, wire.ErrMalformed
	}
	owner, err := h.owner(ctx)
	if err != nil {
		return 434, nil, true, err
	}
	if fields[2] == owner {
		return 0, nil, false, nil
	}
	details, err := h.load(ctx, fields[2])
	if errors.Is(err, social.ErrNotFound) || errors.Is(err, profile.ErrNotFound) {
		return 434, nil, true, &command.GameError{PacketCode: 434, ErrorType: 9003, Message: "This player is unavailable"}
	}
	if err != nil {
		return 434, nil, true, err
	}
	response, found := details[fields[3]]
	if !found {
		return 434, nil, true, &command.GameError{PacketCode: 434, ErrorType: 9003, Message: "This player's supporter slot is empty"}
	}
	return 434, append([]byte(nil), response...), true, nil
}
