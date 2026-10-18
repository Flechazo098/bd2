package eventactions

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
	"sort"
)

func friendshipEpisodeWire(group, id uint64) []byte {
	return wire.AppendVarint(wire.AppendVarint(nil, 1, group), 2, id)
}

func (s *Service) friendshipEpisodeInfo() []byte {
	var completed [][2]uint64
	for k, claimed := range s.state.Claims {
		var group, id uint64
		if claimed {
			if _, err := fmt.Sscanf(k, "friend:%d/%d", &group, &id); err == nil {
				completed = append(completed, [2]uint64{group, id})
			}
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		if completed[i][0] == completed[j][0] {
			return completed[i][1] < completed[j][1]
		}
		return completed[i][0] < completed[j][0]
	})
	var out []byte
	for _, entry := range completed {
		out = wire.AppendBytes(out, 1, friendshipEpisodeWire(entry[0], entry[1]))
	}
	return out
}

func (s *Service) clearFriendshipEpisode(ctx command.Context, request []byte, identity string) ([]byte, error) {
	group, id := val(request, 2), val(request, 3)
	if group == 0 || id == 0 || group > math.MaxInt32 || id > math.MaxInt32 {
		return nil, fmt.Errorf("eventactions: invalid friendship episode group=%d episode=%d", group, id)
	}
	rule, found := s.row("FriendshipSpecialEpisodeTable", 5, 6, group, id)
	if !found {
		return nil, fmt.Errorf("eventactions: unknown friendship episode group=%d episode=%d", group, id)
	}
	clearInfo := friendshipEpisodeWire(group, id)
	claim := "friend:" + key(group, id)
	if s.state.Claims[claim] {
		return wire.AppendBytes(nil, 2, clearInfo), nil
	}
	seasonal := false
	for _, schedule := range s.registry.List() {
		if schedule.Type == 24 && schedule.ID == group && schedule.SubID == id && s.active(schedule) {
			seasonal = true
			break
		}
	}
	var level uint64
	if s.friendship != nil {
		level = s.friendship(group)
	}
	if !seasonal && (level == 0 || level < rule.V(12)) {
		return nil, fmt.Errorf("eventactions: friendship level insufficient group=%d episode=%d level=%d required=%d", group, id, level, rule.V(12))
	}
	if id > 1 && !s.state.Claims["friend:"+key(group, id-1)] {
		return nil, fmt.Errorf("eventactions: previous friendship episode incomplete group=%d episode=%d previous=%d", group, id, id-1)
	}
	rewards := append([]gamedata.Reward(nil), rule.Rewards...)
	if seasonal {
		rewards = append(rewards, rule.EventRewards...)
	}
	bundle, err := s.economy.Apply(ctx, identity, nil, rewards)
	if err != nil {
		return nil, err
	}
	s.state.Claims[claim] = true
	return wire.AppendBytes(wire.AppendBytes(nil, 1, bundle), 2, clearInfo), nil
}
