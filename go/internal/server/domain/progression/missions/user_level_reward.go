package missions

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
)

type levelRewardSnapshot struct {
	Version     string `json:"version"`
	LevelReward uint64 `json:"level_reward"`
}

// AttachUserLevelRewards reads the dedicated claim bucket in the same store as missions.
func (s *Service) AttachUserLevelRewards(ctx command.Context, d *gamedata.AchievementLevelDesign) error {
	if err := d.Validate(); err != nil {
		return err
	}

	entries, ok := s.storage.(stateio.ScopedEntryStore)
	if !ok {
		return fmt.Errorf("missions: user level rewards require entry storage")
	}
	raw, _, err := entries.LoadEntry(ctx.State, "missions", "user_level_rewards", "state")
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

	if s.levelRewards == nil {
		return 0, fmt.Errorf("missions: user level rewards unavailable")
	}
	return s.levelReward, nil
}
