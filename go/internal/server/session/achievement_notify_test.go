package session

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"bd2server/internal/server/cryptox"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/protocol"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bd2server/internal/server/world"
)

func TestAchievementUpdateEmitsAbsoluteNotificationWithoutChangingEmptyResponse(t *testing.T) {
	design := &gamedata.AchievementCounterDesign{Groups: map[int][]int{987: {0}}, Conditions: map[int]gamedata.AchievementCondition{987: {Type: 66}}}
	counter, err := world.NewAchievementService(design, stateio.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	observer, err := world.NewGameplayAchievementObserver(counter, &world.OwnedGameplayAchievementProvider{Design: design})
	if err != nil {
		t.Fatal(err)
	}
	server, _ := NewServer(fakeLogin{}, counter)
	if err := server.AttachResponseObserver(observer); err != nil {
		t.Fatal(err)
	}
	logged := login(t, server)
	request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 987), 3, 1)
	body, _ := cryptox.EncryptBase64Payload(request, server.KeyForTest())
	for attempt := range 2 {
		reply, err := server.DispatchRaw("/AchievementUpdate", []byte(body), "s="+logged.Cookie)
		if err != nil {
			t.Fatal(err)
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(reply.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		response, err := cryptox.DecryptBase64Payload(envelope.Data, server.KeyForTest())
		if err != nil || len(response) != 0 || envelope.PacketCode != 167 {
			t.Fatalf("response=%x code=%d err=%v", response, envelope.PacketCode, err)
		}
		if attempt == 1 {
			if envelope.Notify != "" {
				t.Fatal("replayed update emitted another notification")
			}
			continue
		}
		notify, err := base64.StdEncoding.DecodeString(envelope.Notify)
		if err != nil {
			t.Fatal(err)
		}
		row, _, _ := wire.Bytes(notify, 2)
		group, _, _ := wire.Varint(row, 1)
		value, _, _ := wire.Varint(row, 2)
		isSet, _, _ := wire.Varint(row, 3)
		if group != 987 || value != 1 || isSet != 1 {
			t.Fatalf("notify group=%d value=%d isSet=%d", group, value, isSet)
		}
	}
	value, err := counter.AchievementValue(987)
	if err != nil || value != 1 {
		t.Fatalf("persisted value=%d err=%v", value, err)
	}
}
