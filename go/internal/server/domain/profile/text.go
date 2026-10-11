package profile

import "bd2server/internal/server/domain/command"

const (
	NicknameText = "nickname"
	GreetingText = "greeting"
)

type TextCheck func(command.Context, string, string) error
