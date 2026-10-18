package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/platform/textmoderation"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
)

type textApprovalKey struct{}

type textCandidate struct{ scope, text string }

type textApprovals struct {
	identity command.Identity
	values   map[textCandidate]bool
}

type textField struct {
	scope string
	code  int
	path  []int
}

var playerTextFields = map[string][]textField{
	"/UserNicknameChange":               {{"nickname", 74, []int{2}}},
	"/UserGreetingChange":               {{"greeting", 219, []int{2}}},
	"/MasterTitleInfoUpdate":            {{"master_title", 592, []int{2}}},
	"/PresetSave":                       {{"preset_name", 179, []int{2, 1}}},
	"/PresetInfoChange":                 {{"preset_name", 278, []int{2}}},
	"/EquipPresetSave":                  {{"equipment_preset_name", 254, []int{4}}},
	"/EquipPresetNameChange":            {{"equipment_preset_name", 259, []int{4}}},
	"/EquipMarkSet":                     {{"equipment_mark", 396, []int{3}}},
	"/MonsterHuntPresetSave":            {{"monster_hunt_preset_name", 406, []int{2, 1}}},
	"/MonsterHuntPresetInfoChange":      {{"monster_hunt_preset_name", 0, []int{2}}},
	"/GuildRaidPresetSave":              {{"guild_raid_preset_name", 404, []int{2, 1}}},
	"/GuildRaidPresetInfoChange":        {{"guild_raid_preset_name", 0, []int{2}}},
	"/ColosseumPresetSave":              {{"colosseum_preset_name", 494, []int{2, 1}}},
	"/ColosseumPresetInfoChange":        {{"colosseum_preset_name", 497, []int{2}}},
	"/TotalWarDeckPresetSave":           {{"total_war_preset_name", 240, []int{2, 2}}},
	"/TotalWarPresetInfoChange":         {{"total_war_preset_name", 0, []int{2}}},
	"/GuildCreate":                      {{"guild_name", 296, []int{2}}, {"guild_message", 296, []int{5}}},
	"/GuildInfoEdit":                    {{"guild_message", 305, []int{4}}},
	"/GuildNameChange":                  {{"guild_name", 570, []int{2}}},
	"/GuildNoticeUpdate":                {{"guild_notice", 382, []int{2}}},
	"/SpineInteractionRecordNameUpdate": {{"spine_record_name", 576, []int{3}}},
}

func hasPlayerText(requests []player.Request) bool {
	for _, request := range requests {
		if len(playerTextFields[request.Path]) != 0 {
			return true
		}
	}
	return false
}

func (p *playerInstance) prepareText(ctx context.Context, request player.Command) (*textApprovals, error) {
	if !hasPlayerText(request.Requests) {
		return nil, nil
	}
	if request.Cancellation != nil {
		ctx = request.Cancellation
	}
	approvals := textApprovals{identity: request.Identity, values: map[textCandidate]bool{}}
	for _, item := range request.Requests {
		for _, field := range playerTextFields[item.Path] {
			text := item.Body
			for _, number := range field.path {
				var err error
				text, _, err = wire.Bytes(text, number)
				if err != nil {
					return nil, err
				}
			}
			if !utf8.Valid(text) {
				return nil, wire.ErrMalformed
			}
			candidate := textCandidate{field.scope, string(text)}
			if field.scope == "equipment_mark" {
				mark, _, _ := strings.Cut(candidate.text, "|")
				if strings.HasPrefix(mark, "~") {
					continue
				}
				candidate.text = strings.TrimPrefix(mark, "_")
			}
			if !approvals.values[candidate] && candidate.text != "" {
				if err := p.assembly.checkText(ctx, request.Identity.AccountID, field.scope, candidate.text, field.code); err != nil {
					return nil, err
				}
			}
			approvals.values[candidate] = true
		}
	}
	return &approvals, nil
}

func (p *playerAssembly) checkProfileText(ctx command.Context, scope, text string) error {
	requestContext := ctx.Cancellation
	if requestContext == nil {
		requestContext = context.Background()
	}
	if approvals, ok := requestContext.Value(textApprovalKey{}).(*textApprovals); ok && approvals.identity == ctx.Identity && approvals.values[textCandidate{scope, text}] {
		return nil
	}
	if ctx.State != nil {
		return fmt.Errorf("text moderation was not prepared before the player transaction")
	}
	code := 74
	if scope == profile.GreetingText {
		code = 219
	}
	return p.checkText(requestContext, ctx.AccountID, scope, text, code)
}

func (p *playerAssembly) checkText(ctx context.Context, accountID, scope, text string, code int) error {
	if p.options.textModerator == nil {
		return fmt.Errorf("text moderation is not initialized")
	}
	err := p.options.textModerator.Check(ctx, scope, accountID, text)
	if err == nil {
		return nil
	}
	if errors.Is(err, textmoderation.ErrRejected) {
		return &command.GameError{PacketCode: code, ErrorType: 9001, Message: "Text is not allowed by this server's moderation policy."}
	}
	if errors.Is(err, textmoderation.ErrUnavailable) {
		return &command.GameError{PacketCode: code, ErrorType: 9002, Message: "Text moderation is temporarily unavailable. Please try again."}
	}
	return err
}
