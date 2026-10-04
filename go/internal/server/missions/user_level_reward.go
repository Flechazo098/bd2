package missions

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"encoding/json"
	"fmt"
)

type levelRewardSnapshot struct {
	Version     string `json:"version"`
	LevelReward uint64 `json:"level_reward"`
}

// AttachUserLevelRewards reads the dedicated claim bucket in the same store as missions.
func (s *Service) AttachUserLevelRewards(d *gamedata.AchievementLevelDesign) error {
	if err := d.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, ok := s.storage.(stateio.EntryStore)
	if !ok {
		return fmt.Errorf("missions: user level rewards require entry storage")
	}
	raw, _, err := entries.LoadEntry("missions", "user_level_rewards", "state")
	if err != nil {
		return err
	}
	var state levelRewardSnapshot
	if raw != nil {
		if err := stateio.RequireExactJSONObject(raw, "version", "level_reward"); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		if state.Version != versionconfig.State() {
			return fmt.Errorf("missions: incompatible user level reward version")
		}
		if state.LevelReward != 0 {
			found := false
			for _, l := range d.Levels {
				found = found || l.ID == state.LevelReward
			}
			if !found {
				return fmt.Errorf("missions: claimed user level missing from GameData")
			}
		}
	}
	s.levelRewards = d
	s.levelReward = state.LevelReward
	return nil
}
func (s *Service) LevelRewardCount() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.levelRewards == nil {
		return 0, fmt.Errorf("missions: user level rewards unavailable")
	}
	return s.levelReward, nil
}

func (s *Service) userLevelReward(request []byte) ([]byte, error) {
	if s.levelRewards == nil {
		return nil, fmt.Errorf("missions: user level rewards unavailable")
	}
	if err := requireSeq(request); err != nil {
		return nil, err
	}
	ids, err := packed(request, 2)
	if err != nil || len(ids) == 0 {
		return nil, ErrInvalidRequest
	}
	exp, err := s.achievementExperienceLocked()
	if err != nil {
		return nil, err
	}
	level := s.levelRewards.Level(exp)
	next := s.levelReward
	var fresh []gamedata.AchievementLevel
	var previous uint64
	for _, id := range ids {
		if id == 0 || id <= previous {
			return nil, ErrInvalidRequest
		}
		previous = id
		index := -1
		for i, l := range s.levelRewards.Levels {
			if l.ID == id {
				index = i
				break
			}
		}
		if index < 0 || id > level {
			return nil, ErrInvalidRequest
		}
		if id <= s.levelReward {
			continue
		}
		expected := uint64(0)
		for _, l := range s.levelRewards.Levels {
			if l.ID > next {
				expected = l.ID
				break
			}
		}
		if id != expected {
			return nil, fmt.Errorf("%w: user level reward requires preceding claims", ErrInvalidRequest)
		}
		fresh = append(fresh, s.levelRewards.Levels[index])
		next = id
	}
	var items []player.Item
	var currencies []gamedata.Reward
	for _, l := range fresh {
		granted, err := s.grantRewards(fmt.Sprintf("user-level:%d", l.ID), l.Rewards)
		if err != nil {
			return nil, err
		}
		items = append(items, granted...)
		for _, r := range l.Rewards {
			if r.Type == 2 || r.Type == 3 || r.Type == 4 || r.Type == 12 || r.Type == 20 {
				currencies = append(currencies, r)
			}
		}
	}
	if next != s.levelReward {
		raw, err := json.Marshal(levelRewardSnapshot{versionconfig.State(), next})
		if err != nil {
			return nil, err
		}
		if err := s.storage.(stateio.EntryStore).PutEntry("missions", "user_level_rewards", "state", raw); err != nil {
			return nil, err
		}
		s.levelReward = next
	}
	bundle := rewardBundle(items)
	for _, r := range currencies {
		item := wire.AppendVarint(nil, 3, r.Type)
		item = wire.AppendVarint(item, 4, r.Count)
		bundle = wire.AppendBytes(bundle, 1, item)
	}
	return wire.AppendBytes(nil, 1, bundle), nil
}
