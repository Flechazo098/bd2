package login

import (
	"bd2server/internal/server/domain/command"
	"fmt"
)

type ProfileTitleProvider interface {
	SelectedTitle(command.Context) (uint64, error)
}

func (s *LoginSeed) AttachProfileTitle(provider ProfileTitleProvider) error {
	if provider == nil {
		return fmt.Errorf("account: missing profile title provider")
	}
	s.profileTitle = provider
	return nil
}
