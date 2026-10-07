package pictorial

import "bd2server/internal/server/domain/command"

import "bd2server/internal/server/design/gamedata"

// AttachPermanentBuffs makes reward-owned account buffs available to the same
// Snapshot used by AllCharRefresh, login and authoritative stat consumers.
func (s *Service) AttachPermanentBuffs(provider func(command.Context) ([]gamedata.PictorialBuffStat, error)) {
	s.permanentBuffs = provider
}
