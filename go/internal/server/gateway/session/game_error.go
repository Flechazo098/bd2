package session

import (
	"encoding/json"
	"errors"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/gateway/transport"
	"bd2server/internal/server/protocol"
)

func (s *Server) gameErrorReply(game *gameSession, err error) (transport.RawReply, bool, error) {
	var rejection *command.GameError
	if errors.Is(err, transport.ErrGameSessionExpired) || !errors.As(err, &rejection) || rejection.PacketCode < 0 || rejection.ErrorType <= 0 || rejection.ErrorType >= 9999 {
		return transport.RawReply{}, false, nil
	}
	response, encodeErr := protocol.EnvelopeWithNotify(rejection.PacketCode, nil, game.key, s.now().UnixMilli(), nil)
	if encodeErr != nil {
		return transport.RawReply{}, true, encodeErr
	}
	response.ErrorType = rejection.ErrorType
	response.ErrorMessage = rejection.Message
	body, encodeErr := json.Marshal(response)
	return transport.RawReply{Body: body}, true, encodeErr
}
