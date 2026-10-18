package session

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"errors"
	"fmt"
	"strings"
)

func rejectedRequestError(account string, requests []player.Request, cause error) error {
	var failed *command.RequestError
	if errors.As(cause, &failed) {
		for _, request := range requests {
			sequence, _, _ := wire.Varint(request.Body, 1)
			if request.Path == failed.Path && sequence == failed.Sequence {
				return fmt.Errorf("account_id=%s request={%s}: %w", account, requestErrorContext(request, true), cause)
			}
		}
	}
	const limit = 4
	contexts := make([]string, 0, min(len(requests), limit))
	for _, request := range requests[:min(len(requests), limit)] {
		contexts = append(contexts, requestErrorContext(request, len(requests) == 1))
	}
	return fmt.Errorf("account_id=%s request_count=%d requests=[%s]: %w", account, len(requests), strings.Join(contexts, "; "), cause)
}

type diagnosticField struct {
	number int
	name   string
}

func requestErrorContext(request player.Request, includeFields bool) string {
	sequence, _, _ := wire.Varint(request.Body, 1)
	path := request.Path
	if len(path) > 80 || strings.ContainsAny(path, "\r\n\t") {
		path = "invalid-route"
	}
	context := fmt.Sprintf("path=%s seq=%d", path, sequence)
	if !includeFields {
		return context
	}
	var fields []diagnosticField
	switch request.Path {
	case "/MissionUpdate":
		count := 0
		_ = wire.Walk(request.Body, func(field wire.Field) error {
			if field.Number != 2 || field.Type != 2 {
				return nil
			}
			if count < 3 {
				context += " mission={" + numericErrorFields(field.Value, []diagnosticField{{1, "group_id"}, {2, "mission_id"}, {3, "value"}, {4, "event_id"}}) + "}"
			}
			count++
			return nil
		})
		return context + fmt.Sprintf(" mission_count=%d", count)
	case "/MissionClear":
		fields = []diagnosticField{{3, "group_type"}, {4, "group_id"}, {5, "mission_id"}, {6, "event_id"}}
	case "/PassReward":
		fields = []diagnosticField{{2, "all"}, {3, "pass_id"}, {4, "level_id"}, {5, "reward_type"}}
	case "/PassBuy":
		fields = []diagnosticField{{2, "pass_id"}, {3, "buy_type"}}
	case "/FriendshipSpecialEpisodeClear":
		fields = []diagnosticField{{2, "group_id"}, {3, "episode_id"}}
	case "/FriendshipGift":
		fields = []diagnosticField{{2, "costume_id"}}
	case "/FriendshipCounseling":
		fields = []diagnosticField{{2, "costume_id"}, {3, "session_id"}, {4, "selection"}, {5, "quick"}}
	case "/QuestUpdate", "/QuestClear":
		fields = []diagnosticField{{2, "quest_id"}, {3, "pack_id"}}
	case "/FieldObjectReward":
		fields = []diagnosticField{{2, "pack_id"}, {3, "group_id"}, {4, "object_id"}}
	case "/FieldObjectResearch":
		fields = []diagnosticField{{2, "pack_id"}, {3, "object_id"}}
	case "/SaveUserPosition", "/PackInGameInfo":
		fields = []diagnosticField{{2, "pack_id"}}
	case "/BattleEnter":
		fields = []diagnosticField{{2, "group_id"}, {3, "monster_id"}, {4, "battle_deck"}, {5, "battle_mode"}, {6, "monster_hunt_id"}}
	case "/BattleStart", "/BattleRetry":
		fields = []diagnosticField{{2, "battle_index"}}
	case "/BattleEnd":
		fields = []diagnosticField{{2, "battle_result"}, {4, "pack_id"}}
	case "/BattleRecordInfo", "/BattleRecordSave":
		fields = []diagnosticField{{2, "battle_mode"}, {3, "content_id"}}
	case "/GachaBuy":
		fields = []diagnosticField{{2, "gacha_id"}, {3, "buy_type"}}
	case "/CashShopBuy":
		fields = []diagnosticField{{3, "group_id"}, {6, "event_schedule_id"}}
	case "/CharLevelUp":
		fields = []diagnosticField{{2, "character_index"}}
	case "/UseRandomBox":
		fields = []diagnosticField{{2, "item_index"}, {3, "use_count"}}
	case "/UseResourceItem":
		fields = []diagnosticField{{2, "item_index"}, {3, "use_count"}, {4, "selection"}}
	}
	if values := numericErrorFields(request.Body, fields); values != "" {
		context += " " + values
	}
	return context
}

// Only named scalar fields from the current client protocol enter diagnostics.
func numericErrorFields(body []byte, fields []diagnosticField) string {
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		value, present, err := wire.Varint(body, field.number)
		if err == nil && present {
			values = append(values, fmt.Sprintf("%s=%d", field.name, value))
		}
	}
	return strings.Join(values, " ")
}
