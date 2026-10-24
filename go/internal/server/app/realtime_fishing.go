package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/fishing"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/room"
	"bd2server/internal/server/storage/outbox"
)

func (r *roomResults) FishingRooms(command.Context) ([]fishing.RoomInfo, error) {
	if r.rooms == nil {
		return nil, fmt.Errorf("%w: realtime rooms are not running", room.ErrClosed)
	}
	views := r.rooms.FishingRooms()
	result := make([]fishing.RoomInfo, 0, len(views))
	for _, view := range views {
		result = append(result, fishing.RoomInfo{Code: view.RoomCode, MapID: uint64(view.Selection.MapID), BoatLevel: uint64(view.Selection.BoatLevel), Members: uint64(len(view.Members))})
	}
	return result, nil
}

func (r *roomResults) updateFishing(ctx context.Context, event outbox.Pending) error {
	var projection fishing.Projection
	if err := json.Unmarshal(event.Envelope.Event.Payload, &projection); err != nil {
		return err
	}
	if r.rooms == nil || projection.InstanceID == "" {
		return nil
	}
	view, err := r.rooms.SnapshotForSession(event.Envelope.Source, projection.SessionID, room.Fishing)
	if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrUnauthorized) {
		return nil
	}
	if err != nil {
		return err
	}
	if view.InstanceID != projection.InstanceID {
		return nil
	}
	for _, member := range view.Members {
		if member.Principal.AccountID != event.Envelope.Source {
			continue
		}
		profile := member.Profile
		profile.RodID = int64(projection.Profile.RodID)
		profile.BoatLevel = int64(projection.Profile.BoatLevel)
		profile.BoatSkinID = int64(projection.Profile.BoatSkinID)
		profile.FishingMaps = projection.Profile.Maps
		var buffs [][]byte
		for _, buff := range projection.Buffs {
			raw := wire.AppendVarint(nil, 1, buff.ID)
			raw = wire.AppendVarint(raw, 2, uint64(buff.End))
			buffs = append(buffs, raw)
		}
		deliveries, err := r.rooms.UpdateFishingProjection(ctx, member.Principal, profile, buffs, int64(projection.ItemID), event.Envelope.Event.ID)
		if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrNotReady) {
			return nil
		}
		if err != nil {
			return err
		}
		if r.deliver != nil {
			r.deliver(deliveries)
		}
		return nil
	}
	return nil
}
