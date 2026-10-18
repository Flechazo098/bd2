package notice

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type communityEconomy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}

type CommunityService struct {
	Store   stateio.Store
	Design  *gamedata.CommunityRewards
	Board   *Board
	Economy communityEconomy
	Now     func() time.Time
}

type communityClaim struct {
	Type, SubType uint64
}

func (s CommunityService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	code := 289
	if path == "/CommunityReward" {
		code = 290
	} else if path != "/CommunityRewardInfo" {
		return 0, nil, false, nil
	}
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
		return fail(fmt.Errorf("community: invalid request sequence"))
	}
	if s.Store == nil || s.Design == nil || s.Economy == nil {
		return fail(fmt.Errorf("community: missing dependencies"))
	}
	raw, err := s.Store.Load(ctx.State, "community_rewards")
	if err != nil {
		return fail(err)
	}
	claims := []communityClaim{}
	if raw != nil {
		if err := json.Unmarshal(raw, &claims); err != nil {
			return fail(fmt.Errorf("community: invalid saved claims: %w", err))
		}
	}
	if path == "/CommunityRewardInfo" {
		var out []byte
		for _, claim := range claims {
			row := wire.AppendVarint(nil, 1, claim.Type)
			row = wire.AppendVarint(row, 2, claim.SubType)
			out = wire.AppendBytes(out, 1, row)
		}
		return code, out, true, nil
	}
	typ, _, err := wire.Varint(request, 2)
	if err != nil || typ > 6 {
		return fail(fmt.Errorf("community: invalid reward type"))
	}
	sub, _, err := wire.Varint(request, 3)
	if err != nil || sub > 1<<31-1 || typ != 0 && sub != 0 {
		return fail(fmt.Errorf("community: invalid reward identifier"))
	}
	rewards := s.Design.Community
	if typ == 0 {
		if s.Board == nil || sub == 0 {
			return fail(&command.GameError{PacketCode: code, ErrorType: 1, Message: "survey unavailable"})
		}
		n, found := s.Board.Find(int32(sub))
		now := time.Now()
		if s.Now != nil {
			now = s.Now()
		}
		if !found || n.Type != TypeSurvey || n.SubType <= 0 || now.Before(n.Start) || !now.Before(n.End) {
			return fail(&command.GameError{PacketCode: code, ErrorType: 1, Message: "survey unavailable"})
		}
		sub = uint64(n.SubType)
		rewards = s.Design.Survey
	}
	claim := communityClaim{Type: typ, SubType: sub}
	if slices.ContainsFunc(claims, func(old communityClaim) bool {
		return old.Type == typ && (typ != 0 || old.SubType >= sub)
	}) {
		return code, wire.AppendBytes(nil, 1, nil), true, nil
	}
	bundle, err := s.Economy.Apply(ctx, fmt.Sprintf("community:%d:%d", typ, sub), nil, rewards)
	if err != nil {
		return fail(err)
	}
	claims = append(claims, claim)
	raw, err = json.Marshal(claims)
	if err != nil {
		return fail(err)
	}
	if err := s.Store.Save(ctx.State, "community_rewards", raw); err != nil {
		return fail(err)
	}
	return code, wire.AppendBytes(nil, 1, bundle), true, nil
}
