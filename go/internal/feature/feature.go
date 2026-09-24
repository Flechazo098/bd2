// Package feature contains deliberately small, stateless protocol handlers.
//
// These endpoints were observed to return an empty protobuf for a new local
// account. Keeping that fact here (rather than treating every unknown route
// as successful) lets the session dispatcher remain fail-closed.
package feature

import (
	"errors"
	"fmt"

	"bd2server/internal/wire"
)

// ErrInvalidRequest means a known endpoint was sent a malformed request.
var ErrInvalidRequest = errors.New("feature: invalid protobuf request")

// emptyResponses is the stateless subset of the 2.34.13 new-player batch whose
// successful response protobuf has zero bytes. Stateful routes leave this map
// as soon as their owning domain persists them. Packet codes are protocol
// values, not ordinals derived from the request or response order.
var emptyResponses = map[string]int{
	"/AvatarShopWishListInfo":          470,
	"/CafeteriaInfo":                   351,
	"/CashBonusInfo":                   588,
	"/CharPartnerInfo":                 65,
	"/CommunityRewardInfo":             289,
	"/DailyStoryInfo":                  538,
	"/DatingInfo":                      361,
	"/DispatchInfo":                    0,
	"/EquipPresetInfo":                 253,
	"/EvilCastleDailyRewardState":      441,
	"/EvilCastleTowerInfo":             200,
	"/FieldObjectInfo":                 28,
	"/FieldTrapInfo":                   171,
	"/FireWorksInfo":                   554,
	"/FishingCollectionInfo":           465,
	"/FishingTrapInfo":                 457,
	"/FriendInfoList":                  204,
	"/FriendshipInfo":                  612,
	"/FriendshipSpecialEpisodeInfo":    623,
	"/GuildInitInfo":                   332,
	"/IdCardPresetInfo":                450,
	"/LifeUserInfo":                    594,
	"/MasterTitleInfo":                 590,
	"/MiniEventHubInfo":                534,
	"/MyLikeInfo":                      218,
	"/MyRoomItemInfo":                  232,
	"/PersonalInfo":                    199,
	"/PrestigeSkinInfo":                425,
	"/QuestMaxClearInfo":               137,
	"/SpineInteractionAchievementInfo": 532,
	"/TotalWarRewardState":             252,
	"/TutorialInfo":                    101,
}

// Handle recognizes only audited, stateless empty-response endpoints. The
// request must be a structurally valid protobuf with its normal nonzero
// request sequence in field 1. Unknown paths return ok=false and no response.
// proto is nil on success: protobuf's canonical empty-message encoding is zero
// bytes.
func Handle(path string, request []byte) (packetCode int, proto []byte, ok bool, err error) {
	packetCode, ok = emptyResponses[path]
	if !ok {
		packetCode, proto, ok = initialResponse(path)
	}
	if !ok {
		packetCode, ok = standaloneDefaults[path]
	}
	if !ok {
		packetCode, ok = commandSuccess[path]
		if !ok {
			return 0, nil, false, nil
		}
	}
	sequence, present, walkErr := wire.Varint(request, 1)
	if walkErr != nil || !present || sequence == 0 {
		if walkErr != nil {
			return 0, nil, true, fmt.Errorf("%w: %s: %v", ErrInvalidRequest, path, walkErr)
		}
		return 0, nil, true, fmt.Errorf("%w: %s has no request sequence", ErrInvalidRequest, path)
	}
	return packetCode, proto, true, nil
}

// Service adapts this registry to session.Handler without importing session.
type Service struct{}

func (Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	return Handle(path, request)
}

// EmptyPacketCodes returns a detached copy for diagnostics and protocol tests.
func EmptyPacketCodes() map[string]int {
	result := make(map[string]int, len(emptyResponses))
	for path, code := range emptyResponses {
		result[path] = code
	}
	return result
}
