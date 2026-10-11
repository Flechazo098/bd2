package app

import (
	"encoding/json"
	"errors"

	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/stateio"
)

func executeOutboxCommand(tx outbox.PageStore, request *player.SystemRequest) ([]byte, error) {
	switch request.Kind {
	case player.ReadOutbox:
		if len(request.Body) != 0 {
			return nil, errors.New("outbox read takes no payload")
		}
		pending, err := outbox.Read(tx, 32)
		if err != nil {
			return nil, err
		}
		return json.Marshal(pending)
	case player.AcknowledgeOutbox:
		if err := stateio.RequireExactJSONObject(request.Body, "key", "digest"); err != nil {
			return nil, err
		}
		var receipt struct {
			Key    string `json:"key"`
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal(request.Body, &receipt); err != nil {
			return nil, err
		}
		return nil, outbox.Acknowledge(tx, receipt.Key, receipt.Digest)
	default:
		return nil, errors.New("unknown outbox command")
	}
}
