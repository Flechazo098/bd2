package world

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

type RankStatue struct {
	ID, Season uint64
	Error      bool
	Groups     [][]byte
}
type statueClaim struct {
	Day    string
	Season uint64
}

func (s *Service) AttachRankStatues(view func(command.Context) ([]RankStatue, error)) {
	s.statues = view
}

func (s *Service) statueInfo(ctx command.Context, pack int) ([]byte, error) {
	if s.statues == nil {
		return nil, nil
	}
	design, err := s.presentationSource.FieldStatues(pack)
	if err != nil {
		return nil, err
	}
	if len(design) == 0 {
		return nil, nil
	}
	rows, err := s.statues(ctx)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, row := range rows {
		if _, placed := design[row.ID]; !placed || len(row.Groups) == 0 {
			continue
		}
		b := wire.AppendVarint(nil, 1, row.ID)
		b = wire.AppendVarint(b, 2, row.Season)
		if row.Error {
			b = wire.AppendVarint(b, 3, 1)
		}
		for _, group := range row.Groups {
			b = wire.AppendBytes(b, 4, group)
		}
		out = wire.AppendBytes(out, 14, b)
		claim, err := s.statueClaim(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		if claim.Day == s.statueDay() && claim.Season == row.Season {
			out = wire.AppendVarint(out, 15, row.ID)
		}
	}
	return out, nil
}

func (s *Service) statueDay() string { return s.monsterTime().UTC().Format(time.DateOnly) }
func (s *Service) statueClaim(ctx command.Context, id uint64) (statueClaim, error) {
	var claim statueClaim
	raw, found, err := ctx.State.LoadEntry("missions", "statue_rewards", strconv.FormatUint(id, 10))
	if err != nil || !found {
		return claim, err
	}
	err = json.Unmarshal(raw, &claim)
	return claim, err
}

func (s *Service) handleStatueReward(ctx command.Context, req []byte) (int, []byte, bool, error) {
	fail := func(err error) (int, []byte, bool, error) { return 202, nil, true, err }
	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return fail(ErrInvalidRequest)
	}
	id, found, err := wire.Varint(req, 2)
	if err != nil || !found || id == 0 {
		return fail(ErrInvalidRequest)
	}
	if s.statues == nil || s.researchEconomy == nil {
		return fail(fmt.Errorf("world: statue runtime unavailable"))
	}
	pack, err := s.CurrentPackID(ctx)
	if err != nil {
		return fail(err)
	}
	design, err := s.presentationSource.FieldStatues(pack)
	if err != nil {
		return fail(err)
	}
	reward, placed := design[id]
	if !placed {
		return fail(fmt.Errorf("world: statue absent from current pack"))
	}
	rows, err := s.statues(ctx)
	if err != nil {
		return fail(err)
	}
	var target RankStatue
	for _, row := range rows {
		if row.ID == id && len(row.Groups) > 0 {
			target = row
			break
		}
	}
	if target.Season == 0 || target.Error {
		return fail(fmt.Errorf("world: statue ranking not available"))
	}
	claim, err := s.statueClaim(ctx, id)
	if err != nil {
		return fail(err)
	}
	if claim.Day == s.statueDay() && claim.Season == target.Season {
		return 202, nil, true, nil
	}
	key := fmt.Sprintf("statue:%d:%d:%s", id, target.Season, s.statueDay())
	bundle, err := s.researchEconomy.Apply(ctx, key, nil, []gamedata.Reward{reward})
	if err != nil {
		return fail(err)
	}
	raw, err := json.Marshal(statueClaim{Day: s.statueDay(), Season: target.Season})
	if err != nil {
		return fail(err)
	}
	if err = ctx.State.PutEntry("missions", "statue_rewards", strconv.FormatUint(id, 10), raw); err != nil {
		return fail(err)
	}
	return 202, wire.AppendBytes(nil, 1, bundle), true, nil
}
