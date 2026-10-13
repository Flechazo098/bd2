package monsterhunt

import "strings"

// A settled battle reply remains proof of one completed attempt. Played
// records prove at least one attempt when only season progress was retained.
func (s *Service) RecordedBattleCount() uint64 {
	var replies, played uint64
	for key := range s.state.Replies {
		if strings.HasPrefix(key, "battle:") {
			replies++
		}
	}
	for _, user := range s.state.Users {
		if user.Played {
			played++
		}
	}
	return max(replies, played)
}
