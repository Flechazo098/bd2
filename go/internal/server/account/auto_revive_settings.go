package account

import (
	"bd2server/internal/server/wire"
	"fmt"
)

type AutoReviveSettingsProvider interface{ AutoReviveSettings() (bool, uint64, error) }

func (s *LoginSeed) AttachAutoReviveSettings(p AutoReviveSettingsProvider) error {
	if p == nil {
		return fmt.Errorf("account: nil automatic recovery settings")
	}
	s.autoReviveSettings = p
	return nil
}
func (s *LoginSeed) projectAutoRevive(user []byte) ([]byte, error) {
	if s.autoReviveSettings == nil {
		return user, nil
	}
	on, index, e := s.autoReviveSettings.AutoReviveSettings()
	if e != nil {
		return nil, e
	}
	if index > 9223372036854775807 {
		return nil, fmt.Errorf("account: automatic recovery caster overflow")
	}
	n := uint64(0)
	if on {
		n = 1
	}
	user, _, e = wire.ReplaceVarint(user, 49, n)
	if e != nil {
		return nil, e
	}
	user, _, e = wire.ReplaceVarint(user, 50, index)
	return user, e
}
