package profile

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func (s *AvatarService) ValidateAppearance(ctx command.Context, raw []byte) error {
	var use AvatarUse
	var err error
	use.UseCharID, err = avatarNumber(raw, 1)
	if err != nil {
		return err
	}
	for i := range use.Parts {
		use.Parts[i], err = avatarNumber(raw, i+2)
		if err != nil {
			return err
		}
	}
	if err := wire.Walk(raw, func(wire.Field) error { return nil }); err != nil {
		return err
	}
	owned, err := s.owned(ctx)
	if err != nil {
		return err
	}
	return s.validateUse(use, owned)
}
