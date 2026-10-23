package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/protocol/wire"
)

// missionActionObserver counts mission conditions whose action is one
// successful request with no richer domain event. It runs before the event
// mission observer, so that observer's notification includes the progress.
type missionActionObserver struct{ player *playerAssembly }

// missionActionPaths maps a request to the Define_MissionConditionType it
// performs once.
var missionActionPaths = map[string]uint64{
	"/CafeteriaCumulativeReward": conditions.CafeteriaCumulativeReward,
	"/MyRoomItemInfo":            conditions.MyRoomVisit,
	"/MyRoomLike":                conditions.MyRoomLike,
	"/ShopBuy":                   conditions.BuyItemInShop,
	"/SquareReward":              conditions.SquareReward,
	"/MercenaryScout":            conditions.CharScoutToTargetGrade,
	"/CharSpecialScoutBuy":       conditions.CharScoutToTargetGrade,
}

func (o missionActionObserver) BeforeDispatch(command.Context, string, []byte) error { return nil }

func (o missionActionObserver) AfterDispatch(ctx command.Context, path string, request, _ []byte) ([]byte, error) {
	condition, ok := missionActionPaths[path]
	if path == "/MyRoomUserInfo" {
		// Field 2 is the room owner; the account's own room is a visit.
		owner, err := o.player.ProfileOwner(ctx)
		if err != nil {
			return nil, err
		}
		target, _, _ := wire.Varint(request, 2)
		condition, ok = conditions.MyRoomVisit, target == owner
	}
	if !ok {
		return nil, nil
	}
	return nil, o.player.recordMission(ctx, conditions.Event{Type: condition, Count: 1})
}
