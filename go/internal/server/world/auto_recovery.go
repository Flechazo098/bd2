package world

import (
	"bd2server/internal/server/gamedata"
	"fmt"
)

func (s *Service) AttachAutoRecoveryPolicy(root, version string) error {
	p, e := gamedata.LoadPackRecoveryPolicy(root, version)
	if e != nil {
		return e
	}
	s.autoRecoveryPolicy = p
	return nil
}
func (s *Service) AutoRecoveryAllowed() (bool, error) {
	if s.autoRecoveryPolicy == nil {
		return false, fmt.Errorf("world: automatic recovery pack policy unavailable")
	}
	p, e := s.CurrentPackID()
	if e != nil {
		return false, e
	}
	return s.autoRecoveryPolicy.Allowed(p, s.packCompleteFor(p)), nil
}
