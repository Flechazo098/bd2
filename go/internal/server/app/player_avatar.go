package app

import (
	"fmt"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/profile"
)

func (p *playerAssembly) avatar(ctx command.Context) error {
	var err error
	p.avatarService, err = profile.OpenAvatar(ctx, p.gameplayStore, p.design.avatarProfile, p, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("open avatar profile: %w", err)
	}
	return p.login.AttachAvatarSelection(p.avatarService)
}
func (p *playerAssembly) AvatarItems(ctx command.Context) []assets.Item { return p.ownedItems.All(ctx) }
func (p *playerAssembly) AvatarCurrency(ctx command.Context) assets.Currency {
	return p.wallet.Snapshot(ctx)
}
func (p *playerAssembly) AvatarPriceBalance(ctx command.Context, price gamedata.Reward) (uint64, error) {
	if field, found := events.AdditionalCurrencyFields[price.Type]; found && price.ID == 0 {
		balances, err := p.eventEconomy.AdditionalCurrencies(ctx)
		return balances[field], err
	}
	return 0, fmt.Errorf("avatar: unsupported initial selection currency %d/%d", price.Type, price.ID)
}
func (p *playerAssembly) AvatarUnlocked(ctx command.Context, product gamedata.AvatarProduct) (bool, error) {
	if product.UnlockValue == 0 {
		return true, nil
	}
	switch product.UnlockType {
	case 1:
		return p.worldService.MissionsUnlocked(ctx, product.UnlockValue, 0), nil
	case 2:
		exp, err := p.missionService.AchievementExperience()
		return err == nil && p.design.levelDesign.Level(exp) >= product.UnlockValue, err
	case 6:
		value, err := p.achievementCounters.AchievementValue(ctx, product.UnlockValue)
		return err == nil && value >= product.UnlockSub, err
	default:
		return false, fmt.Errorf("avatar: unsupported design unlock condition %d", product.UnlockType)
	}
}
