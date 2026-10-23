package world

import (
	"fmt"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) handlePacks(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	switch path {
	case "/PackBuy":
		return s.handlePackBuy(ctx, request)
	case "/PackDetailInfo":
		return s.handlePackDetail(ctx, request)
	case "/PackSummaryInfoList":
		return s.handlePackSummary(ctx, request)
	case "/PackRewardObjectCount":
		return s.handlePackRewardCounts(ctx, request)
	case "/PackPreviewInfo", "/PackJamEvent":
		return s.handlePackDocking(ctx, path, request)
	case "/PackInGameInfo":
		return s.handlePackEntry(ctx, request)
	case "/PackInfo":
		seq, found, err := wire.Varint(request, 1)
		if err != nil || !found || seq == 0 {
			return 4, nil, true, fmt.Errorf("%w: PackInfo missing sequence", ErrInvalidRequest)
		}
		body, err := s.accountPackInfo(ctx)
		if err != nil {
			return 4, nil, true, err
		}
		rows, err := s.eventPackInfoRows(ctx)
		if err != nil {
			return 4, nil, true, err
		}
		for _, row := range rows {
			body = wire.AppendBytes(body, 1, row)
		}
		return 4, body, true, nil
	default:
		return 0, nil, false, nil
	}
}

func requestPack(request []byte) (int, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, ErrInvalidRequest
	}
	pack, found, err := wire.Varint(request, 2)
	if err != nil || !found || pack == 0 || pack > uint64(^uint32(0)>>1) {
		return 0, ErrInvalidRequest
	}
	return int(pack), nil
}
