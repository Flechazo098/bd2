package missions

import "bd2server/internal/server/gamedata"

// ClaimedAchievementIDs exposes committed claims to the counter query adapter.
// The mission domain remains the sole owner of reward claims.
func (s *Service) ClaimedAchievementIDs() map[gamedata.AchievementKey]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[gamedata.AchievementKey]bool{}
	for key := range s.design.Achievements {
		if contains(s.state.Claimed, "achievement:"+achievementName(key)) {
			out[key] = true
		}
	}
	return out
}
