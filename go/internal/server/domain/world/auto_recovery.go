package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
)

func (s *Service) AttachAutoRecoveryPolicy(policy *gamedata.PackRecoveryPolicy) error {
	s.autoRecoveryPolicy = policy
	return nil
}
func (s *Service) AutoRecoveryAllowed(ctx command.Context) (bool, error) {
	if s.autoRecoveryPolicy == nil {
		return false, fmt.Errorf("world: automatic recovery pack policy unavailable")
	}
	p, e := s.CurrentPackID(ctx)
	if e != nil {
		return false, e
	}
	return s.autoRecoveryPolicy.Allowed(p, s.packCompleteFor(p)), nil
}
