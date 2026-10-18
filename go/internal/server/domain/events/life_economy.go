package events

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

type LifeEconomy interface {
	CanApply(command.Context, []gamedata.Reward, []gamedata.Reward) error
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}

func (e *Economy) AttachLife(life LifeEconomy) { e.life = life }

func lifeCurrency(typ uint64) bool { return typ >= 64 && typ <= 66 }
