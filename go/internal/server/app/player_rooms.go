package app

import (
	"bd2server/internal/server/runtime/player"
	"context"
)

func (p *playerInstance) prepareRoomResults(ctx context.Context, requests []player.Request) (func(), error) {
	base := p.factory.roomResults
	if base == nil {
		return func() {}, nil
	}
	needed := false
	for _, request := range requests {
		switch request.Path {
		case "/MiniGameActionStart", "/MiniGameActionEnd", "/MiniGameDefenseStart", "/MiniGameDefenseEnd", "/FishingVoyageStart", "/FishingVoyageEnd":
			needed = true
		}
	}
	if !needed {
		return func() {}, nil
	}
	assembly := p.assembly
	runs := assembly.eventPlayService.NativeRuns()
	runs = append(runs, assembly.fishingService.NativeRuns()...)
	prepared, err := base.PrepareResults(ctx, p.accountID, runs)
	if err != nil {
		return nil, err
	}
	assembly.eventPlayService.AttachRooms(prepared)
	assembly.fishingService.AttachRooms(prepared)
	return func() { assembly.eventPlayService.AttachRooms(base); assembly.fishingService.AttachRooms(base) }, nil
}
