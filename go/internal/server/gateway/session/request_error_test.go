package session

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"errors"
	"strings"
	"testing"
)

func TestRejectedRequestContextIdentifiesFailedBatchItemWithoutPayload(t *testing.T) {
	secret := "private-receipt-do-not-log"
	mission := wire.AppendVarint(nil, 1, 303)
	mission = wire.AppendVarint(mission, 2, 3038)
	mission = wire.AppendVarint(mission, 3, 1)
	mission = wire.AppendVarint(mission, 4, 900)
	mission = wire.AppendString(mission, 99, secret)
	body := wire.AppendBytes(wire.AppendVarint(nil, 1, 72), 2, mission)
	gameError := &command.GameError{PacketCode: 1, ErrorType: 2, Message: "unavailable"}
	failure := &command.RequestError{Path: "/MissionUpdate", Sequence: 72, Cause: gameError}
	result := rejectedRequestError("owner", []player.Request{
		{Path: "/CashShopBuy", Body: wire.AppendString(wire.AppendVarint(nil, 1, 71), 7, secret)},
		{Path: "/MissionUpdate", Body: body},
	}, failure)
	message := result.Error()
	for _, want := range []string{"account_id=owner", "path=/MissionUpdate seq=72", "group_id=303", "mission_id=3038", "event_id=900", "value=1"} {
		if !strings.Contains(message, want) {
			t.Errorf("diagnostic %q missing %q", message, want)
		}
	}
	if strings.Contains(message, secret) || strings.Contains(message, "/CashShopBuy") {
		t.Fatalf("diagnostic included an unrelated request or payload: %s", message)
	}
	var preserved *command.GameError
	if !errors.Is(result, failure) || !errors.As(result, &preserved) || preserved != gameError {
		t.Fatal("diagnostic wrapping changed error identity or game error classification")
	}
}

func TestRejectedRequestContextBoundsBatchAndMissionSummaries(t *testing.T) {
	requests := make([]player.Request, 6)
	for i := range requests {
		requests[i] = player.Request{Path: "/QuestUpdate", Body: wire.AppendVarint(nil, 1, uint64(i+1))}
	}
	message := rejectedRequestError("owner", requests, errors.New("commit failed")).Error()
	if !strings.Contains(message, "request_count=6") || strings.Count(message, "path=") != 4 || strings.Contains(message, "seq=5") {
		t.Fatalf("unbounded batch diagnostic: %s", message)
	}
	body := wire.AppendVarint(nil, 1, 10)
	for i := range 5 {
		body = wire.AppendBytes(body, 2, wire.AppendVarint(nil, 2, uint64(i+1)))
	}
	message = requestErrorContext(player.Request{Path: "/MissionUpdate", Body: body}, true)
	if strings.Count(message, "mission={") != 3 || !strings.Contains(message, "mission_count=5") {
		t.Fatalf("unbounded mission diagnostic: %s", message)
	}
}
