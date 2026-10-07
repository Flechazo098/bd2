package eventplay

import (
	"bd2server/internal/server/design/gamedata"
)

func (s *Service) HandlesBattle(mode uint64) bool { return mode == 17 }

func (s *Service) AttachBattleChallenges(d gamedata.EventBattleChallenges) {

	s.battleChallenges = d
}
