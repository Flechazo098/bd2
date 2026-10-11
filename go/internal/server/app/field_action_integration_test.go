//go:build integration

package app

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

// Client 2.36.7 QuestPacket invokes the condition-9 completion callback before
// the asynchronous put-down calls EndInteractionFieldObejct and sends position.
// Read-only GameData 20261008155037: pack5 quest8 is condition9/map52;
// action5200/group520 is type1, Disappear after quest8; action5201 has no quest.
func TestFieldActionQuestClearAcceptsTrailingDropWithoutRestoringExpiredPosition(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if rule := p.design.world.Story.Packs[5].Open; rule != nil && rule.TicketID != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "field-action-chapter-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		for quest := 1; quest < 8; quest++ {
			if err := p.progressState.ClearQuest(ctx, quest, 5, 0); err != nil {
				t.Fatal(err)
			}
		}
	})
	s.protocol(t, "/PackInGameInfo", trapRequest(1, 5))
	questRequest := wire.AppendVarint(trapRequest(2, 8), 3, 5)
	s.protocol(t, "/QuestAccept", questRequest)
	s.position(t, 5, 52)
	questPosition := fieldActionPosition(52, 4.5, 0, -2.25)
	ordinaryPosition := fieldActionPosition(52, 7.25, 0, 3.5)
	drop := func(group, object uint64, position []byte) []byte {
		request := wire.AppendVarint(trapRequest(uint64(s.commands+1), 5), 3, group)
		request = wire.AppendVarint(request, 4, object)
		return wire.AppendBytes(request, 5, position)
	}
	acceptDrop := func(group, object uint64, position []byte) {
		t.Helper()
		reply, err := s.execute("/FieldObjectPositionUpdate", drop(group, object, position))
		if err != nil {
			t.Fatalf("valid put-down for object%d failed: %v", object, err)
		}
		if len(reply.Responses) != 1 || reply.Responses[0].PacketCode != 95 || len(reply.Responses[0].Body) != 0 {
			t.Fatalf("put-down must acknowledge with empty packet95: %+v", reply)
		}
	}
	positions := func() map[uint64][]byte {
		t.Helper()
		body := s.protocol(t, "/FieldObjectInfo", trapRequest(uint64(s.commands+1), 5))
		out := map[uint64][]byte{}
		for _, row := range appRows(body, 2) {
			id, found, err := wire.Varint(row, 1)
			if err != nil || !found || id == 0 {
				t.Fatalf("invalid action identity: %x, %v", row, err)
			}
			position, found, err := wire.Bytes(row, 2)
			if err != nil || !found || out[id] != nil {
				t.Fatalf("invalid or duplicate action position: %x, %v", row, err)
			}
			out[id] = position
		}
		return out
	}
	assertPositions := func(questActive bool) {
		t.Helper()
		got := positions()
		if !bytes.Equal(got[5201], ordinaryPosition) {
			t.Fatalf("ordinary movable object lost its saved drop position: %x", got[5201])
		}
		if questActive {
			if !bytes.Equal(got[5200], questPosition) {
				t.Fatalf("active quest object lost its saved drop position: %x", got[5200])
			}
		} else if _, found := got[5200]; found {
			t.Fatalf("expired quest object must not restore a position on reconnect: %x", got[5200])
		}
	}
	acceptDrop(520, 5200, questPosition)
	acceptDrop(521, 5201, ordinaryPosition)
	assertPositions(true)
	s.reopen(t)
	assertPositions(true)
	update := wire.AppendVarint(trapRequest(uint64(s.commands+1), 8), 3, 5)
	s.protocol(t, "/QuestUpdate", wire.AppendVarint(update, 4, 81))
	clear := s.protocol(t, "/QuestClear", wire.AppendVarint(trapRequest(uint64(s.commands+1), 8), 3, 5))
	if appValue(clear, 3) != 8 {
		t.Fatalf("quest clear did not complete the interacted quest: %x", clear)
	}
	assertPositions(false)
	acceptDrop(520, 5200, fieldActionPosition(52, 10.5, 0, 9.25))
	assertPositions(false)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		stored, err := p.progressState.FieldActionPositions(ctx, 5)
		if err != nil {
			t.Fatal(err)
		}
		if got := stored[5200]; got.QuestCleared || !bytes.Equal(got.Position, questPosition) {
			t.Fatal("trailing drop overwrote the retired quest position")
		}
	})
	s.reopen(t)
	assertPositions(false)

	for _, invalid := range []struct {
		name          string
		group, object uint64
		position      []byte
	}{
		{"wrong-group-after-clear", 521, 5200, questPosition},
		{"unknown-object-after-clear", 520, math.MaxInt32, questPosition},
		{"wrong-current-map-after-clear", 520, 5200, fieldActionPosition(51, 1, 0, 1)},
		{"nonfinite-after-clear", 520, 5200, fieldActionPosition(52, float32(math.Inf(1)), 0, 1)},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			if _, err := s.execute("/FieldObjectPositionUpdate", drop(invalid.group, invalid.object, invalid.position)); err == nil {
				t.Fatal("quest completion bypassed the authored object or position validation")
			}
			assertPositions(false)
		})
	}
	s.position(t, 5, 51)
	if _, err := s.execute("/FieldObjectPositionUpdate", drop(701, 5501, fieldActionPosition(51, 1, 0, 1))); err == nil {
		t.Fatal("object5501 must remain unavailable until its Appear quest45 is complete")
	}
	s.position(t, 5, 52)
	s.reopen(t)
	assertPositions(false)
}

func fieldActionPosition(mapID uint64, x, y, z float32) []byte {
	position := wire.AppendVarint(nil, 1, mapID)
	for i, value := range []float32{x, y, z} {
		position = binary.AppendUvarint(position, uint64((i+2)<<3|5))
		position = binary.LittleEndian.AppendUint32(position, math.Float32bits(value))
	}
	return position
}
