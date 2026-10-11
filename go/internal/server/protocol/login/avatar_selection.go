package login

import "bd2server/internal/server/domain/command"

type AvatarSelectionProvider interface {
	FirstChosen(command.Context) (bool, error)
}

func (s *LoginSeed) AttachAvatarSelection(source AvatarSelectionProvider) error {
	s.avatarSelection = source
	return nil
}
