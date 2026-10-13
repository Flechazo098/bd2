package missions

import (
	"bd2server/internal/server/design/gamedata"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ClaimedAchievementIDs exposes committed claims to the counter query adapter.
// The mission domain remains the sole owner of reward claims.
func (s *Service) ClaimedAchievementIDs() map[gamedata.AchievementKey]bool {

	out := map[gamedata.AchievementKey]bool{}
	for key := range s.design.Achievements {
		if contains(s.state.Claimed, "achievement:"+achievementName(key)) {
			out[key] = true
		}
	}
	return out
}

// AchievementExperience derives exact earned experience from durable claimed
// identities and versioned table rewards. It does not manufacture levels.
func (s *Service) AchievementExperience() (uint64, error) {

	return s.achievementExperienceLocked()
}
func (s *Service) achievementExperienceLocked() (uint64, error) {
	var total uint64
	seen := map[string]bool{}
	for _, identity := range s.state.Claimed {
		if seen[identity] {
			continue
		}
		seen[identity] = true
		name, achievement := strings.CutPrefix(identity, "achievement:")
		if !achievement {
			continue
		}
		parts := strings.Split(name, "/")
		var key gamedata.AchievementKey
		if len(parts) != 3 {
			return 0, fmt.Errorf("missions: invalid claimed achievement")
		}
		for i, target := range []*uint64{&key.ContentsGroup, &key.GroupID, &key.ID} {
			value, err := strconv.ParseUint(parts[i], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("missions: invalid claimed achievement")
			}
			*target = value
		}
		d, known := s.design.Achievements[key]
		if !known || achievementName(key) != name {
			return 0, fmt.Errorf("missions: claimed achievement missing from current GameData")
		}
		if total > math.MaxUint64-d.AddExp {
			return 0, fmt.Errorf("missions: achievement experience overflow")
		}
		total += d.AddExp
	}
	return total, nil
}

func (s *Service) ClaimedMissionCount() uint64 {
	var count uint64
	for _, identity := range s.state.Claimed {
		if strings.HasPrefix(identity, "mission:") {
			count++
		}
	}
	return count
}
