package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/domain/world"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/account"
)

var systemSequence atomic.Uint64

func executePlayerSystem(ctx context.Context, registry *playerRegistry, account string, kind player.SystemKind, body []byte) ([]byte, error) {
	runtime, release, err := registry.Acquire(ctx, account)
	if err != nil {
		return nil, err
	}
	defer release()
	digest := sha256.Sum256(append([]byte{byte(kind)}, body...))
	future, err := runtime.Submit(ctx, player.Command{Identity: command.Identity{AccountID: account, SessionID: "shared-system", RequestID: fmt.Sprint(systemSequence.Add(1))}, Digest: digest, System: &player.SystemRequest{Kind: kind, Body: body}})
	if err != nil {
		return nil, err
	}
	reply, err := future.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if len(reply.Responses) != 1 {
		return nil, errors.New("invalid player system response")
	}
	return reply.Responses[0].Body, nil
}

func (instance *playerInstance) executeSystem(ctx command.Context, tx *accountstate.CommandStore, request *player.SystemRequest) ([]byte, error) {
	p := instance.assembly
	if request.Kind == player.ReadSpineRecording {
		var input spineRecordingDownload
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, err
		}
		return p.interactionRecords().LoadRecordData(ctx, input.ID, input.Token)
	}
	if request.Kind == player.StoryCheckpoint {
		var input world.StoryCheckpointRequest
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, err
		}
		reply, err := p.worldService.StoryCheckpoint(ctx, input)
		if err != nil {
			return nil, err
		}
		return json.Marshal(reply)
	}
	if request.Kind == player.CloseExpiredRaid {
		if instance.sharedSocial == nil {
			return nil, errors.New("guild runtime is unavailable")
		}
		return nil, instance.sharedSocial.CloseExpiredRaid(ctx)
	}
	if request.Kind == player.ReadRoomProfile {
		return p.roomProfile(ctx)
	}
	if request.Kind == player.ClosePVPSeason {
		var closure ranking.Closure
		if err := json.Unmarshal(request.Body, &closure); err != nil {
			return nil, err
		}
		if closure.Owner != ctx.AccountID || p.pvpService == nil {
			return nil, errors.New("invalid PVP closure owner")
		}
		return nil, p.pvpService.CloseSeason(ctx, closure.Mode, closure.Season)
	}
	return executeOutboxCommand(tx, request)
}
