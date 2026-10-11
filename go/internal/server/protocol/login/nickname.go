package login

import "fmt"

type NicknameProvider interface{ CurrentNickname() string }

func (s *LoginSeed) AttachNickname(provider NicknameProvider) error {
	if provider == nil || provider.CurrentNickname() == "" {
		return fmt.Errorf("account: missing nickname provider")
	}
	s.nickname = provider
	return nil
}
