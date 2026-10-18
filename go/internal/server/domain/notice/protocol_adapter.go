package notice

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

const (
	// NoticeInfo has no PacketCodeTypeProto entry; its callback ignores the code.
	noticeInfoCode   = 0
	noticeDetailCode = 195
	errorNotFound    = 9003
)

// Handler answers the news requests made during a game session.
type Handler struct{ Board *Board }

func (h Handler) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/NoticeInfo" && path != "/NoticeDetailInfo" {
		return 0, nil, false, nil
	}
	locale := h.Board.Language(ctx.AccountID)
	switch path {
	case "/NoticeInfo":
		body, err := h.Board.NoticeInfo(request, locale)
		return noticeInfoCode, body, true, err
	case "/NoticeDetailInfo":
		id, _, err := wire.Varint(request, 2)
		if err != nil {
			return 0, nil, true, err
		}
		n, ok := h.Board.Find(int32(id))
		if !ok {
			return 0, nil, true, &command.GameError{PacketCode: noticeDetailCode, ErrorType: errorNotFound, Message: "notice not found"}
		}
		return noticeDetailCode, wire.AppendBytes(nil, 1, encode(n, locale)), true, nil
	}
	return 0, nil, false, nil
}

// NoticeInfo encodes NoticeInfoResponse for NoticeInfoRequest in locale; its
// notice_last_seq=2 selects an incremental answer.
func (b *Board) NoticeInfo(request []byte, locale string) ([]byte, error) {
	last, _, err := wire.Varint(request, 2)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, n := range b.Since(int32(last)) {
		out = wire.AppendBytes(out, 1, encode(n, locale))
	}
	return out, nil
}

// encode writes NoticeDBInfo: id=1 notice_type=2 title=3 thumbnail=4
// start_time=5 end_time=6 web_url=7 notice_contents_info=8 is_pin=10 sort=11.
// Times are Unix milliseconds, as the client compares them with its clock.
func encode(n Notice, locale string) []byte {
	text := n.In(locale)
	out := wire.AppendVarint(nil, 1, uint64(n.ID))
	if n.Type != 0 {
		out = wire.AppendVarint(out, 2, uint64(n.Type))
	}
	out = wire.AppendString(out, 3, text.Title)
	if n.Thumbnail != "" {
		out = wire.AppendString(out, 4, n.Thumbnail)
	}
	out = wire.AppendVarint(out, 5, uint64(n.Start.UnixMilli()))
	out = wire.AppendVarint(out, 6, uint64(n.End.UnixMilli()))
	if n.URL != "" {
		out = wire.AppendString(out, 7, n.URL)
	}
	for _, block := range text.Blocks {
		// NoticeContentsInfo: type=1 (NCT_TEXT 0, NCT_IMAGE 1), value=2.
		var content []byte
		if block.Image {
			content = wire.AppendVarint(content, 1, 1)
		}
		content = wire.AppendString(content, 2, block.Value)
		out = wire.AppendBytes(out, 8, content)
	}
	if n.Pinned {
		out = wire.AppendVarint(out, 10, 1)
	}
	if n.Sort != 0 {
		out = wire.AppendVarint(out, 11, uint64(n.Sort))
	}
	if n.SubType != 0 {
		out = wire.AppendVarint(out, 12, uint64(n.SubType))
	}
	return out
}

// Notify returns the Notify fields every response carries: the board's
// sequence (notice_last_seq=8) and, when maintenance is scheduled, its start
// time (maintenance_start_date=7) in Unix milliseconds.
func Notify(sequence int32, maintenanceStartMS int64) []byte {
	var out []byte
	if maintenanceStartMS > 0 {
		out = wire.AppendVarint(out, 7, uint64(maintenanceStartMS))
	}
	if sequence > 0 {
		out = wire.AppendVarint(out, 8, uint64(sequence))
	}
	return out
}
