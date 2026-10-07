package login

import (
	"bd2server/internal/server/domain/command"
	"fmt"
)

type AutoReviveSettingsProvider interface {
	AutoReviveSettings(command.Context) (bool, uint64, error)
}

func (s *LoginSeed) AttachAutoReviveSettings(p AutoReviveSettingsProvider) error {
	if p == nil {
		return fmt.Errorf("account: nil automatic recovery settings")
	}
	s.autoReviveSettings = p
	return nil
}
