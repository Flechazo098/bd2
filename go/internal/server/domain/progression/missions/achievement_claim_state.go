package missions

import (
	"bd2server/internal/server/design/gamedata"
	"fmt"
	"math"
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
	known := map[string]bool{}
	for key := range s.design.Achievements {
		known["achievement:"+achievementName(key)] = true
	}
	for _, identity := range s.state.Claimed {
		if strings.HasPrefix(identity, "achievement:") && !known[identity] {
			return 0, fmt.Errorf("missions: incompatible claimed achievement %q missing from current GameData", identity)
		}
	}
	for key, d := range s.design.Achievements {
		if !contains(s.state.Claimed, "achievement:"+achievementName(key)) {
			continue
		}
		if total > math.MaxUint64-d.AddExp {
			return 0, fmt.Errorf("missions: achievement experience overflow")
		}
		total += d.AddExp
	}
	return total, nil
}
