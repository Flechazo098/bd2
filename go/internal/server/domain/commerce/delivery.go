package commerce

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
	"time"
)

type CashMailIssuer interface {
	IssueCashOnce(ctx command.Context, _ string, _ uint64, _ []gamedata.Reward, _ time.Time) error
}

func (e *EntitlementEconomy) AttachCashMail(issuer CashMailIssuer) error {
	if issuer == nil {
		return fmt.Errorf("commerce: cash mail issuer unavailable")
	}
	if _, ok := e.graph.(interface {
		ResolveDelivery([]gamedata.BattleReward) (gamedata.CashDelivery, error)
	}); !ok {
		return fmt.Errorf("commerce: cash delivery resolver unavailable")
	}
	e.mail = issuer
	return nil
}
