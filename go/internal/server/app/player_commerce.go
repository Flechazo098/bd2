package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/commerce"
	"bd2server/internal/server/domain/events/tasks"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"time"
)

func (p *playerAssembly) commerce(ctx command.Context) error {
	var err error
	p.cashCatalog = p.design.cashCatalog
	p.cashEconomy, err = commerce.NewEntitlementEconomy(ctx, p.gameplayStore, p.eventEconomy, p.design.cashRewards, p.ownedItems, p.design.cashEntitlementDesign)
	if err != nil {
		return fmt.Errorf("load cash entitlements: %w", err)
	}
	p.cashEconomy.SetClock(time.Now, p.design.eventAPReset.ResetSeconds-9*3600)
	if err := p.mailService.AttachCashRewards(ctx, p.cashEconomy, p.design.cashMailTemplates); err != nil {
		return err
	}
	if err := p.cashEconomy.AttachCashMail(p.mailService); err != nil {
		return err
	}
	p.cashService, err = commerce.NewService(ctx, p.cashCatalog, p.gameplayStore, p.cashEconomy)
	if err != nil {
		return fmt.Errorf("load cash purchase state: %w", err)
	}
	p.clearPackages, err = commerce.NewClearPackages(ctx, p.gameplayStore, p.design.clearPackageDesign, p.cashEconomy, p.ownedItems)
	if err != nil {
		return fmt.Errorf("load clear-package claims: %w", err)
	}
	p.clearPackages.AttachProgress(p.worldService.CashPackagePackCleared, nil)
	p.cashService.SetClock(time.Now, p.design.eventAPReset.ResetSeconds-9*3600)
	if err := p.cashService.AttachPackageRules(p.design.cashDesign.Packages); err != nil {
		return fmt.Errorf("attach cash package progression: %w", err)
	}
	if err := p.cashService.AttachShopSeed(ctx, p.seeds.defaults); err != nil {
		return fmt.Errorf("attach cash product availability: %w", err)
	}
	if err := p.cashService.AttachEventShopSchedules(p.design.cashDesign, p.options.calendars.Events); err != nil {
		return fmt.Errorf("attach event shop availability: %w", err)
	}
	p.cashBonuses, err = commerce.NewCashBonuses(ctx, p.gameplayStore, p.cashEconomy, p.cashService, p.design.cashBonusDesign, p.design.cashDesign.Packages)
	if err != nil {
		return fmt.Errorf("load cash bonus claims: %w", err)
	}
	p.cashService.AttachLegacyCounts(p.gachaService)
	cashSpecialProducts := []gamedata.CashProductKey{{GroupID: p.design.infiniteGacha.ProductGroupID, ProductID: p.design.infiniteGacha.ProductID, SaleGroup: p.design.infiniteGacha.SaleGroup}}
	for _, group := range p.design.regularGacha.Groups() {
		if group.CashProductGroupID != 0 && group.CashProductID != 0 {
			cashSpecialProducts = append(cashSpecialProducts, gamedata.CashProductKey{GroupID: group.CashProductGroupID, ProductID: group.CashProductID, SaleGroup: group.CashSalesGroup})
		}
	}
	if err := p.cashService.AttachSpecialProducts(cashSpecialProducts); err != nil {
		return fmt.Errorf("attach special cash products: %w", err)
	}
	p.cashService.AttachDelegate(func(ctx command.Context, key gamedata.CashProductKey, request []byte) ([]byte, bool, error) {
		known := key.GroupID == p.design.infiniteGacha.ProductGroupID && key.ProductID == p.design.infiniteGacha.ProductID && key.SaleGroup == p.design.infiniteGacha.SaleGroup
		for _, group := range p.design.regularGacha.Groups() {
			if key.GroupID == group.CashProductGroupID && key.ProductID == group.CashProductID && key.SaleGroup == group.CashSalesGroup {
				known = true
				break
			}
		}
		if !known {
			return nil, false, nil
		}
		_, response, handled, err := p.gachaService.Handle(ctx, "/CashShopBuy", request)
		if err != nil || !handled {
			return nil, handled, err
		}
		bundle, _, err := wire.Bytes(response, 1)
		return bundle, true, err
	})
	if err := p.login.AttachPurchaseCounts(p.cashService); err != nil {
		return fmt.Errorf("attach cash purchase counts: %w", err)
	}
	p.eventTasksService, err = eventtasks.Open(ctx, p.gameplayStore, p.design.eventTasksDesign, p.eventRegistry, p.eventEconomy)
	if err != nil {
		return fmt.Errorf("load event tasks state: %w", err)
	}
	if err := p.mailService.AttachAttendanceRewardEconomy(ctx, p.eventEconomy); err != nil {
		return fmt.Errorf("attach attendance mail rewards: %w", err)
	}
	p.eventTasksService.AttachAttendanceMail(p.mailService)
	newbieStep, _, err := wire.Varint(p.login.UserInfo, 39)
	if err != nil {
		return err
	}
	if err = p.eventTasksService.SetNewbieStep(ctx, newbieStep); err != nil {
		return err
	}
	if err = p.login.AttachNewbieStep(p.eventTasksService); err != nil {
		return err
	}
	p.eventTasksService.AttachCashAuthorization(func(ctx command.Context, passID, buyType uint64) bool {
		for _, buy := range p.design.eventTasksDesign.PassBuys[passID] {
			if buy.Type == buyType && buy.CashID != 0 {
				return p.cashService.ConsumeEntitlement(ctx, gamedata.CashProductKey{GroupID: buy.CashGroup, ProductID: buy.CashID, SaleGroup: buy.CashSales})
			}
		}
		return false
	})
	p.eventTasksService.AttachAttendancePremium(func(ctx command.Context, ticket uint64) bool {
		for _, item := range p.ownedItems.All(ctx) {
			if item.Type == 19 && item.ID == ticket && item.Count > 0 && (item.ExpiryTime == 0 || item.ExpiryTime > uint64(time.Now().UnixMilli())) {
				return true
			}
		}
		return false
	})
	p.loginPasses, err = commerce.NewLoginPasses(ctx, p.gameplayStore, p.design.loginPassDesign, p.cashEconomy, p.ownedItems, func(ctx command.Context, group uint64) bool {
		for _, pack := range p.design.cashDesign.Packages {
			if pack.PackageType == 7 && pack.ID == group && p.cashService.IsAvailable(ctx, gamedata.CashProductKey{GroupID: pack.GroupID, ProductID: pack.ID, SaleGroup: pack.SaleGroup}) {
				return true
			}
		}
		return false
	})
	if err != nil {
		return fmt.Errorf("load login-pass progress: %w", err)
	}
	p.loginPasses.SetClock(time.Now, p.design.eventAPReset.ResetSeconds-9*3600)
	p.eventTasksService.AttachUnlockResolver(p.worldService.MissionsUnlocked)
	if err = p.missionService.AttachEventHandler(ctx, p.eventTasksService); err != nil {
		return fmt.Errorf("attach mission event handler: %w", err)
	}

	return nil
}
